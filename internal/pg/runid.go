package pg

// Run-attribution GUC: the capture trigger reads it in-transaction; CommitTurn
// sets it with SET LOCAL (#206).

// RunIDSetting is the per-session GUC carrying a run's id; must match
// capture.go's current_setting read.
const RunIDSetting = "iris.run_id"
