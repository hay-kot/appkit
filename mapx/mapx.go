// Package mapx provides small generic helpers for mapping values between
// types, including slice and error-propagating variants.
package mapx

// MapFunc is a generic mapper that converts a T into a U. Since it is itself
// a function type, it can be invoked directly; the methods below are for
// applying it over slices or composing it with error-returning calls.
type MapFunc[T any, U any] func(T) U

// Slice applies the mapper to each element of v, returning a new slice of U.
// The returned slice has the same length as v; nil input yields nil output.
func (m MapFunc[T, U]) Slice(v []T) []U {
	if v == nil {
		return nil
	}
	out := make([]U, len(v))
	for i := range v {
		out[i] = m(v[i])
	}
	return out
}

// Err composes the mapper with an error-returning call: if err is non-nil, the
// zero U and err are returned; otherwise the mapped value and nil.
//
// Typical usage:
//
//	return toDTO.Err(repo.Find(ctx, id))
func (m MapFunc[T, U]) Err(v T, err error) (U, error) {
	if err != nil {
		var zero U
		return zero, err
	}
	return m(v), nil
}

// SliceErr composes the mapper with an error-returning call that produces a
// slice. If err is non-nil, nil and err are returned; otherwise the mapped
// slice and nil.
//
// Typical usage:
//
//	return toDTO.SliceErr(repo.List(ctx))
func (m MapFunc[T, U]) SliceErr(v []T, err error) ([]U, error) {
	if err != nil {
		return nil, err
	}
	return m.Slice(v), nil
}
