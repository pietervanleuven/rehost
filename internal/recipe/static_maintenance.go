package recipe

import (
	"context"

	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// A static site has no application to freeze, so maintenance mode is a no-op
// success — there is nothing to lock and nothing that can be left locked.

func (Static) EnableMaintenance(context.Context, db.Host, detect.Install) (MaintenanceResult, error) {
	return staticNoop(), nil
}

func (Static) DisableMaintenance(context.Context, db.Host, detect.Install) (MaintenanceResult, error) {
	return staticNoop(), nil
}

func (Static) MaintenanceStatus(context.Context, db.Host, detect.Install) (MaintenanceState, error) {
	return MaintenanceOff, nil
}

func staticNoop() MaintenanceResult {
	return MaintenanceResult{State: MaintenanceOff, Method: "noop", Supported: true, Note: "static site has no maintenance mode"}
}
