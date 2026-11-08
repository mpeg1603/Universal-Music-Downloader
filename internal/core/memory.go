package core

import (
	"runtime/debug"
)

// TuneMemory is called once by the programs at the start. Go normally lets its heap grow to
// twice what's in use before collecting; with this much long-lived data (the library index, every
// track) that's hundreds of megabytes of nothing. Collecting earlier costs very little now that
// the steady work allocates little.
func TuneMemory() { debug.SetGCPercent(40) }

// releaseMemory gives memory back to the system after a big one-off job (importing, a library
// check, sorting), instead of keeping it around for the next one.
func releaseMemory() { go debug.FreeOSMemory() }
