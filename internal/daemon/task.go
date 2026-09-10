package daemon

import "github.com/artyomsv/quil/internal/hookevents"

// taskObserve is filled in by the task registry (see task.go in the next
// task); the ledger call site in emitEvent is wired first so the two land as
// one seam.
func (d *Daemon) taskObserve(pane *Pane, e PaneEvent, tr hookevents.Transition) {}
