package bearer

import (
	"time"

	"slp/pkg/session"
)

// BearerMode defines the policy for selecting User Plane vs Control Plane
type BearerMode string

const (
	BearerModeUPOnly      BearerMode = "UP_ONLY"
	BearerModeCPOnly      BearerMode = "CP_ONLY"
	BearerModeUPPreferred BearerMode = "UP_PREFERRED"
	BearerModeCPPreferred BearerMode = "CP_PREFERRED"
	BearerModeAuto        BearerMode = "AUTO" // Alias for UP_PREFERRED
)

// BearerUsed indicates which bearer successfully completed the positioning request
type BearerUsed string

const (
	BearerUP BearerUsed = "USER_PLANE"
	BearerCP BearerUsed = "CONTROL_PLANE"
)

// LocationRequest contains the input parameters for a ProvideLocation request
type LocationRequest struct {
	TargetID    string         // MSISDN or subscriber ID
	Bearer      BearerMode     // Selected bearer mode
	QoP         session.QoP    // Quality of Position requirements
	Timeout     time.Duration  // Max duration to wait for fix
	AuthKey     []byte         // Optional per-subscriber auth key
}

// LocationResponse contains the final outcome of the location procedure
type LocationResponse struct {
	Status           string                  // "SUCCESS" or "FAILED"
	BearerUsed       BearerUsed              // USER_PLANE or CONTROL_PLANE
	FallbackOccurred bool                    // True if secondary bearer was engaged
	Location         *session.LocationResult // Final calculated coordinates
	Duration         time.Duration           // Total roundtrip duration
	ErrorMsg         string                  // Error description if failed
}
