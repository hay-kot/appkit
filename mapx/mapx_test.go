package mapx_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/hay-kot/appkit/mapx"
)

type (
	user    struct{ ID int }
	userDTO struct{ ID string }
)

var toDTO mapx.MapFunc[user, userDTO] = func(u user) userDTO {
	return userDTO{ID: strconv.Itoa(u.ID)}
}

func TestMapFunc_DirectCall(t *testing.T) {
	got := toDTO(user{ID: 42})
	if got.ID != "42" {
		t.Errorf("want 42, got %q", got.ID)
	}
}

func TestMapFunc_Slice(t *testing.T) {
	got := toDTO.Slice([]user{{ID: 1}, {ID: 2}, {ID: 3}})
	want := []userDTO{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMapFunc_SliceNilInputYieldsNil(t *testing.T) {
	if got := toDTO.Slice(nil); got != nil {
		t.Errorf("nil input should yield nil, got %v", got)
	}
}

func TestMapFunc_SliceEmptyInputYieldsEmpty(t *testing.T) {
	got := toDTO.Slice([]user{})
	if got == nil || len(got) != 0 {
		t.Errorf("empty input should yield empty (not nil) slice, got %v", got)
	}
}

func TestMapFunc_ErrPassesErrorThrough(t *testing.T) {
	sentinel := errors.New("repo down")
	_, err := toDTO.Err(user{ID: 1}, sentinel)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel, got %v", err)
	}
}

func TestMapFunc_ErrMapsOnSuccess(t *testing.T) {
	dto, err := toDTO.Err(user{ID: 99}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dto.ID != "99" {
		t.Errorf("want 99, got %q", dto.ID)
	}
}

func TestMapFunc_SliceErrPassesErrorThrough(t *testing.T) {
	sentinel := errors.New("boom")
	got, err := toDTO.SliceErr([]user{{ID: 1}}, sentinel)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel, got %v", err)
	}
	if got != nil {
		t.Errorf("want nil slice on error, got %v", got)
	}
}

func TestMapFunc_SliceErrMapsOnSuccess(t *testing.T) {
	got, err := toDTO.SliceErr([]user{{ID: 10}, {ID: 20}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "10" || got[1].ID != "20" {
		t.Errorf("unexpected result: %+v", got)
	}
}
