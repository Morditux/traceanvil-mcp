package debugger

import (
	"reflect"
	"testing"
)

func TestBackendSpecs(t *testing.T) {
	gdb, err := backendSpecFor(BackendGDB)
	if err != nil {
		t.Fatal(err)
	}
	if gdb.AdapterID != "gdb" || gdb.InitializedAfterLaunch || gdb.DefaultThreadFromProcess {
		t.Errorf("GDB spec = %+v", gdb)
	}
	if len(gdb.Args) == 0 || gdb.HardeningCheck == nil {
		t.Errorf("GDB spec is missing hardening: %+v", gdb)
	}

	lldb, err := backendSpecFor(BackendLLDB)
	if err != nil {
		t.Fatal(err)
	}
	if lldb.AdapterID != "lldb" || !lldb.InitializedAfterLaunch || !lldb.DefaultThreadFromProcess {
		t.Errorf("LLDB spec = %+v", lldb)
	}
	if !reflect.DeepEqual(lldb.Args, lldbCommandArgs()) {
		t.Errorf("LLDB args = %v, want %v", lldb.Args, lldbCommandArgs())
	}

	if _, err := backendSpecFor("other"); err == nil {
		t.Fatal("backendSpecFor(other) accepted an unknown backend")
	}
}
