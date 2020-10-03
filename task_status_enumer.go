// Code generated by "enumer -type=TaskStatus -trimprefix=TaskStatus -trimprefix=TaskStatus -output task_status_enumer.go"; DO NOT EDIT.

//
package workers

import (
	"fmt"
)

const _TaskStatusName = "UndefinedWaitProcessSuccessFailRepeatWaitCancel"

var _TaskStatusIndex = [...]uint8{0, 9, 13, 20, 27, 31, 41, 47}

func (i TaskStatus) String() string {
	if i < 0 || i >= TaskStatus(len(_TaskStatusIndex)-1) {
		return fmt.Sprintf("TaskStatus(%d)", i)
	}
	return _TaskStatusName[_TaskStatusIndex[i]:_TaskStatusIndex[i+1]]
}

var _TaskStatusValues = []TaskStatus{0, 1, 2, 3, 4, 5, 6}

var _TaskStatusNameToValueMap = map[string]TaskStatus{
	_TaskStatusName[0:9]:   0,
	_TaskStatusName[9:13]:  1,
	_TaskStatusName[13:20]: 2,
	_TaskStatusName[20:27]: 3,
	_TaskStatusName[27:31]: 4,
	_TaskStatusName[31:41]: 5,
	_TaskStatusName[41:47]: 6,
}

// TaskStatusString retrieves an enum value from the enum constants string name.
// Throws an error if the param is not part of the enum.
func TaskStatusString(s string) (TaskStatus, error) {
	if val, ok := _TaskStatusNameToValueMap[s]; ok {
		return val, nil
	}
	return 0, fmt.Errorf("%s does not belong to TaskStatus values", s)
}

// TaskStatusValues returns all values of the enum
func TaskStatusValues() []TaskStatus {
	return _TaskStatusValues
}

// IsATaskStatus returns "true" if the value is listed in the enum definition. "false" otherwise
func (i TaskStatus) IsATaskStatus() bool {
	for _, v := range _TaskStatusValues {
		if i == v {
			return true
		}
	}
	return false
}
