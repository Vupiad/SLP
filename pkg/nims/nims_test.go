package nims_test

import (
	"context"
	"testing"

	"slp/pkg/nims"
)

func ptr[T any](v T) *T {
	return &v
}

func TestNimsCellLookup(t *testing.T) {
	db := nims.NewDatabase()
	ctx := context.Background()

	// 1. Lookup 5G cell
	nrCell := nims.CellIdentifier{
		RadioType: nims.Radio5GNR,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   1001,
		CellID:    987654321,
	}

	pos, err := db.LookupCell(ctx, nrCell)
	if err != nil {
		t.Fatalf("LookupCell 5G failed: %v", err)
	}
	if pos.Latitude != 21.013589 || pos.Longitude != 105.527311 {
		t.Fatalf("5G coordinates mismatch: %+v", pos)
	}

	// 2. Lookup 4G cell with Timing Advance
	lteCellWithTA := nims.CellIdentifier{
		RadioType: nims.Radio4GLTE,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   1002,
		CellID:    1234567,
		TA:        ptr(int64(2)), // 2 steps TA
	}
	posTA, err := db.LookupCell(ctx, lteCellWithTA)
	if err != nil {
		t.Fatalf("LookupCell 4G with TA failed: %v", err)
	}
	if posTA.Uncertainty <= 0 {
		t.Fatal("expected positive uncertainty with TA")
	}

	// 3. Lookup 2G GSM cell
	gsmCell := nims.CellIdentifier{
		RadioType: nims.Radio2GGSM,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   100,
		CellID:    500,
	}
	posGsm, err := db.LookupCell(ctx, gsmCell)
	if err != nil {
		t.Fatalf("LookupCell 2G failed: %v", err)
	}
	if posGsm.CellName != "HNI_HOANKIEM_2G_01" {
		t.Fatalf("cell name mismatch: %s", posGsm.CellName)
	}

	// 4. Fallback for unknown Viettel cell
	unknownCell := nims.CellIdentifier{
		RadioType: nims.Radio5GNR,
		MCC:       452,
		MNC:       4,
		TAC_LAC:   9999,
		CellID:    88888,
	}
	posUnknown, err := db.LookupCell(ctx, unknownCell)
	if err != nil {
		t.Fatalf("expected fallback for Viettel MCC, got err: %v", err)
	}
	if posUnknown.Latitude == 0 {
		t.Fatal("expected valid fallback coordinates")
	}

	// 5. Foreign cell not found
	foreignCell := nims.CellIdentifier{
		RadioType: nims.Radio5GNR,
		MCC:       999,
		MNC:       99,
		TAC_LAC:   1,
		CellID:    1,
	}
	_, err = db.LookupCell(ctx, foreignCell)
	if err == nil {
		t.Fatal("expected error for unknown foreign cell")
	}
}
