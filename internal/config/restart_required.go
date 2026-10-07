package config

import (
	"errors"
	"fmt"
)

// ErrRestartRequired identifies runtime configuration changes that cannot be
// applied safely to a running process.
var ErrRestartRequired = errors.New("configuration change requires service restart")

// RestartRequiredError describes a configuration boundary that may only change
// during process startup.
type RestartRequiredError struct {
	Component string
	Reason    string
}

func (e *RestartRequiredError) Error() string {
	if e == nil {
		return ErrRestartRequired.Error()
	}
	component := e.Component
	if component == "" {
		component = "runtime configuration"
	}
	if e.Reason == "" {
		return fmt.Sprintf("%s: %s", component, ErrRestartRequired)
	}
	return fmt.Sprintf("%s: %s: %s", component, ErrRestartRequired, e.Reason)
}

func (e *RestartRequiredError) Unwrap() error {
	return ErrRestartRequired
}
