package dispatcher

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/mrsmtvd/go-workers"
	"github.com/mrsmtvd/go-workers/manager"
)

type SimpleDispatcherResult struct {
	workerItem *manager.WorkersManagerItem
	taskItem   *manager.TasksManagerItem
	result     interface{}
	err        error
	cancel     bool
}

type SimpleDispatcher struct {
	wg sync.WaitGroup

	_ [4]byte // atomic requires 64-bit alignment for struct field access
	workers.StatusItemBase

	ctx       context.Context
	ctxCancel context.CancelFunc

	workers   workers.Manager
	tasks     workers.Manager
	listeners *manager.ListenersManager

	allowExecuteTasks       chan struct{}
	tickerAllowExecuteTasks *workers.Ticker
	results                 chan SimpleDispatcherResult
}

func NewSimpleDispatcher() *SimpleDispatcher {
	return NewSimpleDispatcherWithContext(context.Background())
}

func NewSimpleDispatcherWithContext(ctx context.Context) *SimpleDispatcher {
	d := &SimpleDispatcher{
		workers:                 manager.NewWorkersManager(),
		tasks:                   manager.NewTasksManager(),
		listeners:               manager.NewListenersManager(),
		allowExecuteTasks:       make(chan struct{}, 1),
		tickerAllowExecuteTasks: workers.NewTicker(time.Second),
		results:                 make(chan SimpleDispatcherResult),
	}

	d.setStatusDispatcher(workers.DispatcherStatusWait)

	d.ctx, d.ctxCancel = context.WithCancel(ctx)
	return d
}

func (d *SimpleDispatcher) Context() context.Context {
	return d.ctx
}

func (d *SimpleDispatcher) Run() error {
	if !d.IsStatus(workers.DispatcherStatusWait) {
		return errors.New("Dispatcher is running")
	}

	d.setStatusDispatcher(workers.DispatcherStatusProcess)

	go d.doResultCollector()
	go d.doDispatch()
	d.notifyAllowExecuteTasks()

	<-d.ctx.Done()
	d.setStatusDispatcher(workers.DispatcherStatusCancel)

	for _, w := range d.workers.GetAll() {
		d.setStatusWorker(w, workers.WorkerStatusCancel)
	}

	for _, t := range d.tasks.GetAll() {
		d.setStatusTask(t, workers.WorkerStatusCancel)
	}

	d.wg.Wait()
	d.setStatusDispatcher(workers.DispatcherStatusWait)

	return nil
}

func (d *SimpleDispatcher) Cancel() error {
	d.ctxCancel()
	return d.ctx.Err()
}

func (d *SimpleDispatcher) Metadata() workers.Metadata {
	return workers.Metadata{
		workers.DispatcherMetadataStatus: d.Status(),
	}
}

func (d *SimpleDispatcher) Status() workers.DispatcherStatus {
	return workers.DispatcherStatus(d.StatusInt64())
}

func (d *SimpleDispatcher) SetStatus(status workers.Status) {
	panic("Change status nof allowed")
}

func (d *SimpleDispatcher) AddWorker(worker workers.Worker) error {
	item := manager.NewWorkersManagerItem(worker, workers.WorkerStatusWait)
	err := d.workers.Push(item)
	if err != nil {
		return err
	}

	d.listeners.AsyncTrigger(d.Context(), workers.EventWorkerAdd, worker, item.Metadata())
	d.notifyAllowExecuteTasks()
	return nil
}

func (d *SimpleDispatcher) RemoveWorker(worker workers.Worker) {
	item := d.workers.GetById(worker.Id())
	if item != nil {
		d.setStatusWorker(item, workers.WorkerStatusCancel)

		workerItem := item.(*manager.WorkersManagerItem)
		workerItem.Cancel()
		workerItem.SetTask(nil)

		d.workers.Remove(item)
		d.listeners.AsyncTrigger(d.Context(), workers.EventWorkerRemove, workerItem.Worker(), workerItem.Metadata())
	}
}

func (d *SimpleDispatcher) GetWorkerMetadata(id string) workers.Metadata {
	if item := d.workers.GetById(id); item != nil {
		return item.Metadata()
	}

	return nil
}

func (d *SimpleDispatcher) GetWorkers() []workers.Worker {
	all := d.workers.GetAll()
	collection := make([]workers.Worker, 0, len(all))

	for _, item := range all {
		collection = append(collection, item.(*manager.WorkersManagerItem).Worker())
	}

	return collection
}

func (d *SimpleDispatcher) AddTask(task workers.Task) error {
	item := manager.NewTasksManagerItem(task, workers.TaskStatusWait)
	err := d.tasks.Push(item)
	if err != nil {
		return err
	}

	d.listeners.AsyncTrigger(d.Context(), workers.EventTaskAdd, task, item.Metadata())
	d.notifyAllowExecuteTasks()
	return nil
}

func (d *SimpleDispatcher) RemoveTask(task workers.Task) {
	item := d.tasks.GetById(task.Id())
	if item != nil {
		d.setStatusTask(item, workers.TaskStatusCancel)

		taskItem := item.(*manager.TasksManagerItem)
		taskItem.Cancel()

		d.tasks.Remove(item)
		d.listeners.AsyncTrigger(d.Context(), workers.EventTaskRemove, taskItem.Task(), taskItem.Metadata())
	}
}

func (d *SimpleDispatcher) GetTaskMetadata(id string) workers.Metadata {
	if item := d.tasks.GetById(id); item != nil {
		return item.Metadata()
	}

	return nil
}

func (d *SimpleDispatcher) GetTasks() []workers.Task {
	all := d.tasks.GetAll()
	collection := make([]workers.Task, 0, len(all))

	for _, item := range all {
		collection = append(collection, item.(*manager.TasksManagerItem).Task())
	}

	return collection
}

func (d *SimpleDispatcher) AddListener(eventId workers.Event, listener workers.Listener) error {
	d.listeners.Attach(eventId, listener)
	d.listeners.AsyncTrigger(d.Context(), workers.EventListenerAdd, eventId, listener, d.GetListenerMetadata(listener.Id()))

	return nil
}

func (d *SimpleDispatcher) RemoveListener(eventId workers.Event, listener workers.Listener) {
	item := d.listeners.GetById(listener.Id())
	if item != nil {
		d.listeners.DeAttach(eventId, listener)
		d.listeners.AsyncTrigger(d.Context(), workers.EventListenerRemove, eventId, listener, item.Metadata())
	}
}

func (d *SimpleDispatcher) GetListenerMetadata(id string) workers.Metadata {
	if item := d.listeners.GetById(id); item != nil {
		return item.Metadata()
	}

	return nil
}

func (d *SimpleDispatcher) GetListeners() []workers.Listener {
	return d.listeners.Listeners()
}

func (d *SimpleDispatcher) doResultCollector() {
	d.wg.Add(1)
	defer d.wg.Done()

	for {
		select {
		case result := <-d.results:
			result.taskItem.SetCancel(nil)
			result.workerItem.SetCancel(nil)

			if d.IsStatus(workers.DispatcherStatusCancel) {
				continue
			}

			result.workerItem.SetTask(nil)

			if !result.cancel || !result.workerItem.IsStatus(workers.WorkerStatusCancel) {
				d.setStatusWorker(result.workerItem, workers.WorkerStatusWait)
				if err := d.workers.Push(result.workerItem); err != nil {
					log.Printf("Push worker failed with error: %s", err.Error())
				}
			}

			if !result.cancel && !result.taskItem.IsStatus(workers.TaskStatusCancel) {
				if result.err != nil {
					d.setStatusTask(result.taskItem, workers.TaskStatusFail)
				} else {
					d.setStatusTask(result.taskItem, workers.TaskStatusSuccess)
				}

				if repeats := result.taskItem.Task().Repeats(); repeats < 0 || result.taskItem.Attempts() < repeats {
					repeatInterval := result.taskItem.Task().RepeatInterval()
					if repeatInterval > 0 {
						result.taskItem.SetAllowStartAt(time.Now().Add(repeatInterval))
					}

					d.setStatusTask(result.taskItem, workers.TaskStatusRepeatWait)
					if err := d.tasks.Push(result.taskItem); err != nil {
						log.Printf("Push task failed with error: %s", err.Error())
					}
				} else {
					d.tasks.Remove(result.taskItem)
				}
			}

			d.listeners.AsyncTrigger(d.Context(), workers.EventTaskExecuteStop, result.taskItem.Task(), result.taskItem.Metadata(), result.workerItem.Worker(), result.workerItem.Metadata(), result.result, result.err)
			d.notifyAllowExecuteTasks()

		case <-d.ctx.Done():
			return
		}
	}
}

func (d *SimpleDispatcher) doDispatch() {
	d.wg.Add(1)
	defer d.wg.Done()

	for {
		select {
		case <-d.allowExecuteTasks:
			d.doExecuteTasks()

		case <-d.tickerAllowExecuteTasks.C():
			d.doExecuteTasks()

		case <-d.ctx.Done():
			d.tickerAllowExecuteTasks.Stop()
			return
		}
	}
}

func (d *SimpleDispatcher) doExecuteTasks() {
	if !d.IsStatus(workers.DispatcherStatusProcess) {
		return
	}

	for {
		pullWorker := d.workers.Pull()
		pullTask := d.tasks.Pull()

		if pullWorker != nil && pullTask != nil {
			castWorker := pullWorker.(*manager.WorkersManagerItem)
			castTask := pullTask.(*manager.TasksManagerItem)

			d.listeners.AsyncTrigger(d.Context(), workers.EventTaskExecuteStart, castTask.Task(), castTask.Metadata(), castWorker.Worker(), castWorker.Metadata())
			go d.doRunTask(castWorker, castTask)
		} else {
			if pullWorker != nil {
				_ = d.workers.Push(pullWorker)
			}

			if pullTask != nil {
				_ = d.tasks.Push(pullTask)
			}

			return
		}
	}
}

func (d *SimpleDispatcher) doRunTask(workerItem *manager.WorkersManagerItem, taskItem *manager.TasksManagerItem) {
	d.wg.Add(1)
	defer d.wg.Done()

	task := taskItem.Task()

	workerItem.SetTask(task)
	d.setStatusWorker(workerItem, workers.WorkerStatusProcess)

	taskItem.SetAttempts(taskItem.Attempts() + 1)
	d.setStatusTask(taskItem, workers.TaskStatusProcess)

	now := time.Now()
	if taskItem.Attempts() == 1 {
		taskItem.SetFirstStartedAt(now)
	}
	taskItem.SetLastStartedAt(now)

	ctx := workers.NewContextWithAttempt(d.ctx, taskItem.Attempts())

	var ctxCancel context.CancelFunc

	timeout := task.Timeout()
	if timeout > 0 {
		ctx, ctxCancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, ctxCancel = context.WithCancel(ctx)
	}

	defer ctxCancel()
	taskItem.SetCancel(ctxCancel)
	workerItem.SetCancel(ctxCancel)

	done := make(chan SimpleDispatcherResult, 1)

	go func() {
		defer func() {
			if err := recover(); err != nil {
				done <- SimpleDispatcherResult{
					workerItem: workerItem,
					taskItem:   taskItem,
					result:     err,
					err:        errors.New("Panic recovered"),
				}
			}
		}()

		result, err := workerItem.Worker().RunTask(ctx, task)

		done <- SimpleDispatcherResult{
			workerItem: workerItem,
			taskItem:   taskItem,
			result:     result,
			err:        err,
		}
	}()

	select {
	case <-ctx.Done():
		// TODO:
		//<-done

		d.results <- SimpleDispatcherResult{
			workerItem: workerItem,
			taskItem:   taskItem,
			err:        ctx.Err(),
			cancel:     ctx.Err() == context.Canceled,
		}

	case r := <-done:
		d.results <- r
	}
}

func (d *SimpleDispatcher) notifyAllowExecuteTasks() {
	if d.IsStatus(workers.DispatcherStatusProcess) && len(d.allowExecuteTasks) == 0 {
		d.allowExecuteTasks <- struct{}{}
	}
}

func (d *SimpleDispatcher) setStatusDispatcher(status workers.Status) {
	last := d.Status()
	d.StatusItemBase.SetStatus(status)
	d.listeners.AsyncTrigger(d.Context(), workers.EventDispatcherStatusChanged, d, status, last)
}

func (d *SimpleDispatcher) setStatusWorker(worker workers.ManagerItem, status workers.Status) {
	last := worker.Status()
	worker.SetStatus(status)
	item := worker.(*manager.WorkersManagerItem)
	d.listeners.AsyncTrigger(d.Context(), workers.EventWorkerStatusChanged, item.Worker(), item.Metadata(), status, last)
}

func (d *SimpleDispatcher) setStatusTask(task workers.ManagerItem, status workers.Status) {
	last := task.Status()
	task.SetStatus(status)
	item := task.(*manager.TasksManagerItem)
	d.listeners.AsyncTrigger(d.Context(), workers.EventTaskStatusChanged, item.Task(), item.Metadata(), status, last)
}

func (d *SimpleDispatcher) SetTickerExecuteTasksDuration(t time.Duration) {
	d.tickerAllowExecuteTasks.SetDuration(t)
}
