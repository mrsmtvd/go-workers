package dispatcher

import (
	"container/heap"
	"errors"
	"sync"

	"github.com/kihamo/go-workers/collection"
	"github.com/kihamo/go-workers/task"
	"github.com/kihamo/go-workers/worker"
	"github.com/pivotal-golang/clock"
)

const (
	DispatcherStatusWait = int64(iota)
	DispatcherStatusProcess
)

type Dispatcher struct {
	mutex sync.RWMutex
	wg    *sync.WaitGroup
	clock clock.Clock

	workers   *collection.Workers
	tasks     *collection.Tasks
	waitTasks *collection.Tasks

	status      int64
	workersBusy int

	newQueue     chan task.Tasker // очередь новых заданий
	executeQueue chan task.Tasker // очередь выполняемых заданий

	doneTask        chan task.Tasker   // канал уведомления о завершении выполнения заданий
	doneWorker      chan worker.Worker // канал уведомления о завершении рабочего
	quit            chan bool          // канал для завершения диспетчера
	allowProcessing chan bool          // канал для блокировки выполнения новых задач для случая, когда все исполнители заняты
}

func NewDispatcher() *Dispatcher {
	return NewDispatcherWithClock(clock.NewClock())
}

func NewDispatcherWithClock(c clock.Clock) *Dispatcher {
	return &Dispatcher{
		wg:    new(sync.WaitGroup),
		clock: c,

		workers:   collection.NewWorkers(),
		tasks:     collection.NewTasks(),
		waitTasks: collection.NewTasks(),

		status:      DispatcherStatusWait,
		workersBusy: 0,

		newQueue:     make(chan task.Tasker),
		executeQueue: make(chan task.Tasker),

		doneWorker:      make(chan worker.Worker),
		quit:            make(chan bool, 1),
		allowProcessing: make(chan bool),
	}
}

func (d *Dispatcher) Run() error {
	if d.GetStatus() == DispatcherStatusProcess {
		return errors.New("Dispatcher is running")
	}

	d.setStatus(DispatcherStatusProcess)

	defer func() {
		d.setStatus(DispatcherStatusWait)
	}()

	// отслеживание квоты на занятость исполнителей
	go func() {
		for {
			d.executeQueue <- <-d.newQueue

			<-d.allowProcessing
		}
	}()

	for d.waitTasks.Len() > 0 {
		d.AddTask(d.waitTasks.Pop().(task.Tasker))
	}

	heap.Init(d.workers)

	for {
		select {
		// пришел новый таск на выполнение от flow контроллера
		case t := <-d.executeQueue:
			worker := heap.Pop(d.workers).(worker.Worker)
			d.runWorker(worker)
			worker.SendTask(t)
			heap.Push(d.workers, worker)

			// проверяем есть ли еще свободные исполнители для задач
			if d.workersBusy++; d.workersBusy < d.workers.Len() {
				d.allowProcessing <- true
			}

		// пришло уведомление, что рабочий закончил выполнение задачи
		case w := <-d.doneWorker:
			t := w.GetTask()
			d.tasks.RemoveById(t.GetId())

			if d.doneTask != nil {
				d.doneTask <- t
			}

			heap.Remove(d.workers, d.workers.GetIndexById(w.GetId()))
			heap.Push(d.workers, w)

			w.Reset()

			repeats := t.GetRepeats()
			if repeats == -1 || t.GetAttempts() < repeats {
				t.SetStatus(task.TaskStatusRepeatWait)
				d.AddTask(t)
			}

			// проверяем не освободился ли какой-нибудь исполнитель
			if d.workersBusy--; d.workersBusy == d.workers.Len()-1 {
				d.allowProcessing <- true
			}

		case <-d.quit:
			d.wg.Wait()
			return nil
		}
	}
}

func (d *Dispatcher) AddWorker() worker.Worker {
	w := worker.NewWorkmanWithClock(d.doneWorker, d.GetClock())
	heap.Push(d.workers, w)

	return w
}

func (d *Dispatcher) runWorker(w worker.Worker) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		w.Run()
	}()
}

func (d *Dispatcher) GetWorkers() *collection.Workers {
	return d.workers
}

func (d *Dispatcher) AddTask(t task.Tasker) {
	add := func() {
		if d.GetStatus() == DispatcherStatusProcess {
			d.tasks.Push(t)
			d.newQueue <- t
		} else {
			d.waitTasks.Push(t)
		}
	}

	duration := t.GetDuration()
	if duration > 0 {
		timer := d.GetClock().NewTimer(duration)
		go func() {
			<-timer.C()
			add()
		}()
	} else {
		add()
	}
}

func (d *Dispatcher) AddNamedTaskByFunc(n string, f task.TaskFunction, a ...interface{}) task.Tasker {
	task := task.NewTask(f, a...)

	if n != "" {
		task.SetName(n)
	}

	d.AddTask(task)

	return task
}

func (d *Dispatcher) AddTaskByFunc(f task.TaskFunction, a ...interface{}) task.Tasker {
	return d.AddNamedTaskByFunc("", f, a...)
}

func (d *Dispatcher) GetTasks() *collection.Tasks {
	return d.tasks
}

func (d *Dispatcher) GetWaitTasks() *collection.Tasks {
	return d.waitTasks
}

func (d *Dispatcher) Kill() error {
	if d.GetStatus() == DispatcherStatusProcess {
		d.quit <- true
		return nil
	}

	return errors.New("Dispatcher isn't running")
}

func (d *Dispatcher) GetStatus() int64 {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.status
}

func (d *Dispatcher) setStatus(s int64) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.status = s
}

func (d *Dispatcher) GetClock() clock.Clock {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.clock
}

func (d *Dispatcher) SetTaskDoneChannel(c chan task.Tasker) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.doneTask = c
}
