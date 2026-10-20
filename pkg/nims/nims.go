package nims

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

// RadioType represents the cellular radio access technology
type RadioType string

const (
	Radio5GNR   RadioType = "5G_NR"
	Radio4GLTE  RadioType = "4G_LTE"
	Radio3GWCDMA RadioType = "3G_WCDMA"
	Radio2GGSM  RadioType = "2G_GSM"
)

// CellIdentifier defines cell identification keys
type CellIdentifier struct {
	RadioType RadioType
	MCC       int64  // Mobile Country Code (e.g. 452 for Vietnam)
	MNC       int64  // Mobile Network Code (e.g. 4 for Viettel)
	TAC_LAC   int64  // Tracking Area Code or Location Area Code
	CellID    int64  // NCI (5G), ECI/CI (4G/2G), UC (3G)
	TA        *int64 // Optional Timing Advance
}

func (c CellIdentifier) StringKey() string {
	if c.RadioType == Radio3GWCDMA {
		return fmt.Sprintf("%s:%d-%d-%d", c.RadioType, c.MCC, c.MNC, c.CellID)
	}
	return fmt.Sprintf("%s:%d-%d-%d-%d", c.RadioType, c.MCC, c.MNC, c.TAC_LAC, c.CellID)
}

// GeoPosition represents resolved geographic location coordinates
type GeoPosition struct {
	Latitude    float64 // Decimal degrees
	Longitude   float64 // Decimal degrees
	Altitude    float64 // Height in meters above sea level
	Uncertainty float64 // Accuracy/uncertainty radius in meters
	Confidence  int64   // Confidence percentage (e.g. 90%)
	CellName    string  // Tower/Site name in NIMS
}

// NimsClient is the client interface to the NIMS database
type NimsClient interface {
	LookupCell(ctx context.Context, cell CellIdentifier) (*GeoPosition, error)
}

// Database implements an in-memory NIMS database
type Database struct {
	mu    sync.RWMutex
	cells map[string]GeoPosition
}

// NewDatabase initializes a NIMS database with default Viettel 5G/4G/2G cell sites
func NewDatabase() *Database {
	db := &Database{
		cells: make(map[string]GeoPosition),
	}
	db.populateDefaultSites()
	return db
}

func (db *Database) populateDefaultSites() {
	// Sample Viettel cells (MCC=452, MNC=4)
	// Site 1: Viettel High Tech / CNC - Hoa Lac, Hanoi (21.013589, 105.527311)
	db.RegisterCell(CellIdentifier{
		RadioType: Radio5GNR,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   1001,
		CellID:    987654321,
	}, GeoPosition{
		Latitude:    21.013589,
		Longitude:   105.527311,
		Altitude:    22.0,
		Uncertainty: 50.0,
		Confidence:  95,
		CellName:    "HNI_HOALAC_5G_01",
	})

	// Site 2: Viettel Headquarters - Keangnam / Cau Giay, Hanoi (21.028511, 105.804817)
	db.RegisterCell(CellIdentifier{
		RadioType: Radio4GLTE,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   1002,
		CellID:    1234567,
	}, GeoPosition{
		Latitude:    21.028511,
		Longitude:   105.804817,
		Altitude:    30.0,
		Uncertainty: 100.0,
		Confidence:  90,
		CellName:    "HNI_CAUGIAY_4G_02",
	})

	// Site 3: Viettel 2G GSM Cell (MCC=452, MNC=4, LAC=100, CI=500)
	db.RegisterCell(CellIdentifier{
		RadioType: Radio2GGSM,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   100,
		CellID:    500,
	}, GeoPosition{
		Latitude:    21.031200,
		Longitude:   105.852300,
		Altitude:    15.0,
		Uncertainty: 300.0,
		Confidence:  85,
		CellName:    "HNI_HOANKIEM_2G_01",
	})

	// Site 4: Viettel 3G WCDMA Cell (MCC=452, MNC=4, LAC=200, CI=600)
	db.RegisterCell(CellIdentifier{
		RadioType: Radio3GWCDMA,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   200,
		CellID:    600,
	}, GeoPosition{
		Latitude:    10.776530,
		Longitude:   106.700980,
		Altitude:    10.0,
		Uncertainty: 200.0,
		Confidence:  90,
		CellName:    "HCM_Q1_3G_01",
	})
}

// RegisterCell adds or updates a cell definition in NIMS
func (db *Database) RegisterCell(cell CellIdentifier, pos GeoPosition) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.cells[cell.StringKey()] = pos
}

// LookupCell resolves cell information to coordinates, adjusting uncertainty with TA if available
func (db *Database) LookupCell(ctx context.Context, cell CellIdentifier) (*GeoPosition, error) {
	db.mu.RLock()
	pos, found := db.cells[cell.StringKey()]
	db.mu.RUnlock()

	if !found {
		// If MCC/MNC matches Vietnam (452-04), fallback to reasonable cell estimate
		if cell.MCC == 452 {
			defaultPos := GeoPosition{
				Latitude:    21.028511,
				Longitude:   105.804817,
				Altitude:    20.0,
				Uncertainty: 500.0,
				Confidence:  80,
				CellName:    fmt.Sprintf("VIETTEL_DEFAULT_%s", cell.RadioType),
			}
			return &defaultPos, nil
		}
		return nil, fmt.Errorf("cell not found in NIMS database: %s", cell.StringKey())
	}

	result := pos

	// Apply Timing Advance (TA) refinement if present:
	// In LTE/NR, 1 TA step corresponds to approximately 78.12 meters (round trip / 2 * c).
	// In GSM, 1 TA step is approx 550 meters.
	if cell.TA != nil && *cell.TA > 0 {
		var stepMeters float64
		switch cell.RadioType {
		case Radio5GNR, Radio4GLTE:
			stepMeters = 78.12
		case Radio2GGSM:
			stepMeters = 550.0
		default:
			stepMeters = 100.0
		}
		distance := float64(*cell.TA) * stepMeters
		// Refine uncertainty based on TA ring
		result.Uncertainty = math.Min(result.Uncertainty, distance*0.5+50.0)
	}

	return &result, nil
}

var ErrCellNotFound = errors.New("cell not found in nims")
