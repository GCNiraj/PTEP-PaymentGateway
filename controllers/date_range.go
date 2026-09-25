package controllers

import (
	"errors"
	"strings"
	"time"
)

// genericQueryError is what a caller is told when a query fails. The driver's
// own text — table names, SQLSTATE codes, the engine — goes to the log.
const genericQueryError = "Something went wrong"

// dateLayout is the only form these endpoints accept. It is also the form the
// dashboard's date inputs produce, so nothing legitimate is turned away.
const dateLayout = "2006-01-02"

// errInvalidDateRange is the single answer to every bad from/to pair. What was
// wrong with it is deliberately not said: "2026-08'-24" reached Postgres
// unparsed and came back as `invalid input syntax for type date ... SQLSTATE
// 22007`, which told a caller the database engine, that the column is a date,
// and that their input reached SQL at all (ASD Cyber Security, 23 September
// 2026, finding V4).
var errInvalidDateRange = errors.New("Invalid date range")

// parseDateRange checks from/to before either goes near a query.
//
// Both are optional — an absent bound means "unbounded", which is what the
// stats screens ask for on first load. What is not allowed is a value that is
// present and not a date, or a range that ends before it starts.
//
// It returns the trimmed values so the caller passes on exactly what was
// validated rather than the raw string it still has in hand.
func parseDateRange(from, to string) (string, string, error) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)

	var fromDate, toDate time.Time
	var err error

	if from != "" {
		if fromDate, err = time.Parse(dateLayout, from); err != nil {
			return "", "", errInvalidDateRange
		}
	}
	if to != "" {
		if toDate, err = time.Parse(dateLayout, to); err != nil {
			return "", "", errInvalidDateRange
		}
	}
	if from != "" && to != "" && toDate.Before(fromDate) {
		return "", "", errInvalidDateRange
	}

	return from, to, nil
}
