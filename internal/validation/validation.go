// Package validation holds the one validator instance this service uses to
// check request bodies after BodyParser.
//
// WHY THIS EXISTS. The admin console accepted whatever was typed into it:
// "<h1>HTML Injection</h1>" was a valid app name and "986543210saasa" was a
// valid phone number (ASD Cyber Security, 23 September 2026, finding V2).
// Nothing downstream re-checked them, so the malformed value was stored, and
// the dashboard was the only thing standing between it and a reader.
//
// The rules live here rather than in each controller so that create and edit
// cannot drift apart — an edit endpoint that validates less than its create
// endpoint is the same hole with an extra step.
package validation

import (
	"errors"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
)

// safeTextPattern is what a human-entered name may contain: letters in any
// script (\p{L}, so Dzongkha and Hindi names are not rejected), digits, and the
// punctuation that occurs in real business names — "Lhaki Hotels & Resorts
// (Pvt.) Ltd." passes, "<h1>x</h1>" does not, because < > are absent.
var safeTextPattern = regexp.MustCompile(`^[\p{L}\p{N} .,&'()\-_/]+$`)

// phonePattern allows an optional leading + and nothing but digits after it.
// Length is checked separately so the error can say which rule was broken.
var phonePattern = regexp.MustCompile(`^\+?[0-9]+$`)

// merchantRefPattern is the shape of a merchant reference. It must match
// payment_merchant_reference on the booking platform exactly, so the character
// set is kept to what survives a URL, a log line and a bank's own field without
// being rewritten anywhere in between.
var merchantRefPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// accountPattern is a bank account number: digits, nothing else.
var accountPattern = regexp.MustCompile(`^[0-9]{6,20}$`)

var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())

	// safetext: the character set above. Registered rather than written as a
	// regex in every tag so the pattern is compiled once and stated once.
	_ = v.RegisterValidation("safetext", func(fl validator.FieldLevel) bool {
		value := fl.Field().String()
		if value == "" {
			// Emptiness is `required`'s business, not this rule's. Saying so
			// here too would make an optional field impossible to leave blank.
			return true
		}
		return safeTextPattern.MatchString(value)
	})

	// phonedigits: digits only, optional leading +, 8–15 digits. The count
	// excludes the +, because the + is not a digit.
	_ = v.RegisterValidation("phonedigits", func(fl validator.FieldLevel) bool {
		value := fl.Field().String()
		if value == "" {
			return true
		}
		if !phonePattern.MatchString(value) {
			return false
		}
		digits := strings.TrimPrefix(value, "+")
		return len(digits) >= 8 && len(digits) <= 15
	})

	// merchantref: the reference the booking platform chose for a property.
	_ = v.RegisterValidation("merchantref", func(fl validator.FieldLevel) bool {
		value := fl.Field().String()
		if value == "" {
			return true
		}
		return merchantRefPattern.MatchString(value)
	})

	return v
}

// MerchantReference reports whether a merchant reference is well formed. It is
// exported for the handlers that receive the value loose rather than on a
// struct — CreateMapping takes it as one field of three.
func MerchantReference(value string) bool { return merchantRefPattern.MatchString(value) }

// AccountNumber reports whether a beneficiary account number is 6 to 20 digits.
func AccountNumber(value string) bool { return accountPattern.MatchString(value) }

// SafeText reports whether a human-entered name is within the permitted
// character set. Exported for values that arrive in a map rather than a struct,
// such as the credential payloads.
func SafeText(value string) bool { return safeTextPattern.MatchString(value) }

// ErrInvalid is what every failure here reduces to at the edge. Controllers
// return a fixed message to the client and never the underlying text: a
// validation error names the field and the rule, which is fine, but it can also
// carry the rejected value, and echoing rejected input back into a response is
// how a reflected payload finds a reader.
var ErrInvalid = errors.New("invalid input")

// Struct validates a request body and reports which field failed.
//
// The returned message names the field and what was expected. It never quotes
// the submitted value — see ErrInvalid.
func Struct(payload any) error {
	err := validate.Struct(payload)
	if err == nil {
		return nil
	}

	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return ErrInvalid
	}

	var fieldErrors validator.ValidationErrors
	if errors.As(err, &fieldErrors) && len(fieldErrors) > 0 {
		return errors.New(describe(fieldErrors[0]))
	}
	return ErrInvalid
}

// describe turns one field failure into a sentence an administrator can act on,
// built only from the field name and the rule — never from the input.
func describe(fe validator.FieldError) string {
	field := fe.Field()
	switch fe.Tag() {
	case "required":
		return field + " is required"
	case "min":
		return field + " must be at least " + fe.Param() + " characters"
	case "max":
		return field + " must be at most " + fe.Param() + " characters"
	case "email":
		return field + " must be a valid email address"
	case "safetext":
		return field + " may contain only letters, digits, spaces and . , & ' ( ) - _ /"
	case "phonedigits":
		return field + " must be 8 to 15 digits, optionally starting with +"
	case "oneof":
		return field + " must be one of: " + fe.Param()
	default:
		return field + " is not valid"
	}
}
