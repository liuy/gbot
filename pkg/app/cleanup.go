package app

// Cleanup tears down all resources held by the Instance: closes REPL
// sessions, all engines in the manager, media stores, and removes the
// PID file. Safe to call on a partially-initialized Instance (nil fields
// are skipped).
func (inst *Instance) Cleanup() {
	if inst.MainRefs != nil && inst.MainRefs.REPL != nil {
		inst.MainRefs.REPL.Close()
	}
	if inst.EngineMgr != nil {
		for _, vs := range inst.EngineMgr.List() {
			if vs.Engine != nil {
				vs.Engine.Close()
			}
		}
	}
	for _, ms := range inst.MediaStores {
		if ms != nil {
			ms.Close()
		}
	}
	if inst.PIDCleanup != nil {
		inst.PIDCleanup()
	}
}
