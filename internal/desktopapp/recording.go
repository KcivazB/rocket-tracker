package desktopapp

import (
	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/application"
	"rocket-tracker/internal/domain"
)

// configuredRecorder linearizes selection and commit with every cooperating
// setup writer (including the separate CLI). Uploader memory may lag setup.
func configuredRecorder(records application.LocalRecords, path string) application.Recorder {
	return application.Recorder{Records: records, WithDestination: func(commit func(*domain.Destination) error) error {
		return agent.WithConfigSnapshot(path, func(cfg *agent.Config) error {
			if cfg == nil {
				return commit(nil)
			}
			d := domain.Destination{Server: cfg.Server, ServerID: cfg.ServerID, AccountID: cfg.AccountID}
			return commit(&d)
		})
	}}
}
