//go:build windows

package mcp

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// job is the job object this process put itself in; it stays open while the process lives.
var job windows.Handle

// EndChildrenWithProcess makes every program this process starts, and every program those
// start, however far down, end when this process ends, whatever the way it ends (E4 on
// Windows). It puts the process in a job object that ends its processes when it closes, and
// the system closes it when the process is gone: the programs started after this call are born
// in the job. A tool server started by `npx` or `uvx` is a program that starts the program, and
// without the job the second one would be left running when Metagente stopped.
//
// It is called once, at the start of the process, before any program is started. It does not
// replace the end of each program that the pool asks for while running: it only makes sure
// that nothing is left behind once the process has ended.
func EndChildrenWithProcess() error {
	j, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(j, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(j)
		return err
	}
	if err := windows.AssignProcessToJobObject(j, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(j)
		return err
	}
	job = j // never closed: the system closes it, and ends the job, when this process ends
	return nil
}
