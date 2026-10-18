package server

import (
	"rocket-tracker/internal/application"
	"rocket-tracker/internal/store"
)

func validateMatchQuantities(m *store.Match) error { return application.ValidateMatchQuantities(m) }
