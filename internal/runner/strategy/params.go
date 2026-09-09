// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package strategy

import (
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const formatDateTime = "datetime"

type Column struct {
	rule       api.ColumnRule
	params     api.StrategyParams
	consistent bool
	onNull     api.ValueHandling
	onEmpty    api.ValueHandling
	constant   *string
}

// OutputLength reports an exact generated length, excluding retained input values.
func (c *Column) OutputLength() (int, bool) {
	switch c.rule.Strategy {
	case api.StrategyHash, api.StrategyDigits, api.StrategyAlphanumeric:
		return int(*c.params.Length), true
	case api.StrategyPhone:
		return int(*c.params.Digits), true
	case api.StrategyDate:
		if c.params.Format == formatDateTime {
			return len(time.DateTime), true
		}
		return len(time.DateOnly), true
	case api.StrategyUUID:
		return 36, true
	case api.StrategyVATID:
		return len(c.params.Country) + 9, true
	case api.StrategyIBAN:
		return 22, true
	default:
		return 0, false
	}
}

func Validate(rule api.ColumnRule, defaults api.PolicyDefaults) error {
	_, err := Compile(rule, defaults, nil)
	return err
}

// Compile copies caller-owned values; resolved constants remain outside the policy snapshot.
func Compile(rule api.ColumnRule, defaults api.PolicyDefaults, resolvedConstant *string) (*Column, error) {
	if !slices.Contains(names, string(rule.Strategy)) {
		return nil, unknown(string(rule.Strategy))
	}
	c := &Column{rule: *rule.DeepCopy(), consistent: consistentDefault(rule.Strategy)}
	if rule.Params != nil {
		c.params = *rule.Params.DeepCopy()
	}
	if rule.Consistent != nil {
		if rule.Strategy == api.StrategyNull || rule.Strategy == api.StrategyMask || rule.Strategy == api.StrategyConstant {
			return nil, fmt.Errorf("strategy %q does not accept consistent", rule.Strategy)
		}
		c.consistent = *rule.Consistent
	}
	c.onNull, c.onEmpty = rule.OnNull, rule.OnEmpty
	if c.onNull == "" {
		c.onNull = defaults.OnNull
	}
	if c.onEmpty == "" {
		c.onEmpty = defaults.OnEmpty
	}
	for _, handling := range []api.ValueHandling{c.onNull, c.onEmpty} {
		if handling != "" && handling != api.ValueHandlingKeep && handling != api.ValueHandlingGenerate {
			return nil, errors.New("value handling must be Keep or Generate")
		}
	}
	p := &c.params
	if p.Locale == "" {
		p.Locale = defaults.Locale
	}
	if p.Locale == "" {
		p.Locale = "en"
	}
	switch rule.Strategy {
	case api.StrategyFirstName, api.StrategyLastName, api.StrategyFullName, api.StrategyJobTitle,
		api.StrategyAddress, api.StrategyStreetAddress, api.StrategyStreetSuffix,
		api.StrategyCity, api.StrategyPostalCode, api.StrategyState, api.StrategyCountry:
		if p.Locale != "en" {
			return nil, errors.New("this strategy supports locale en")
		}
	}
	if p.Domain == "" {
		p.Domain = defaults.EmailDomain
	}
	if p.Length != nil && (*p.Length < 1 || *p.Length > 4096) {
		return nil, errors.New("length must be between 1 and 4096")
	}
	if err := c.prepareParams(resolvedConstant); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Column) prepareParams(resolvedConstant *string) error {
	p := &c.params
	switch c.rule.Strategy {
	case api.StrategyAlphanumeric, api.StrategyDigits:
		return prepareLength(c.rule.Strategy, p)
	case api.StrategyPhone:
		return preparePhone(p)
	case api.StrategyEmail:
		return prepareEmail(p)
	case api.StrategyDate:
		return prepareDate(p)
	case api.StrategyVATID, api.StrategyIBAN:
		return prepareCountry(p)
	case api.StrategyNumber:
		return prepareNumber(p)
	case api.StrategyText:
		return prepareText(p)
	case api.StrategyConstant:
		return c.prepareConstant(resolvedConstant)
	case api.StrategyHash:
		return prepareHash(p)
	case api.StrategyMask:
		return prepareMask(p)
	}
	return nil
}

func prepareLength(name api.Strategy, p *api.StrategyParams) error {
	if p.Length == nil {
		return fmt.Errorf("strategy %q requires length", name)
	}
	if p.Case != "" && p.Case != "Mixed" && p.Case != caseUpper && p.Case != "Lower" {
		return errors.New("case must be Mixed, Upper or Lower")
	}
	return nil
}

func preparePhone(p *api.StrategyParams) error {
	if p.Digits == nil {
		p.Digits = new(int32(15))
	}
	if *p.Digits < 1 || *p.Digits > 15 {
		return errors.New("phone digits must be between 1 and 15")
	}
	return nil
}

func prepareEmail(p *api.StrategyParams) error {
	if p.Domain != "" {
		if p.ASCII == nil || *p.ASCII {
			for _, character := range p.Domain {
				if character > 127 {
					return errors.New("ASCII email requires an ASCII domain")
				}
			}
		}
		address, err := mail.ParseAddress("anonymous@" + p.Domain)
		if err != nil || address.Address != "anonymous@"+p.Domain || strings.ContainsAny(p.Domain, "\r\n /@") {
			return errors.New("email domain is invalid")
		}
	}
	return nil
}

func prepareDate(p *api.StrategyParams) error {
	if p.From == "" {
		p.From = "1950-01-01"
	}
	from, err := time.Parse(time.DateOnly, p.From)
	if err != nil {
		return errors.New("date from must use YYYY-MM-DD")
	}
	if p.To != "" {
		to, parseErr := time.Parse(time.DateOnly, p.To)
		if parseErr != nil || to.Before(from) {
			return errors.New("date to must use YYYY-MM-DD and not precede from")
		}
	}
	if p.Format != "" && p.Format != string(api.StrategyDate) && p.Format != formatDateTime {
		return errors.New("date format must be date or datetime")
	}
	return nil
}

func prepareCountry(p *api.StrategyParams) error {
	if p.Country == "" {
		p.Country = "DE"
	}
	if p.Country != "DE" {
		return errors.New("vatId and iban currently support country DE")
	}
	return nil
}

func prepareNumber(p *api.StrategyParams) error {
	if p.Min == nil {
		p.Min = new(int64(0))
	}
	if p.Max == nil {
		p.Max = new(int64(100))
	}
	if *p.Min > *p.Max {
		return errors.New("number min must not exceed max")
	}
	return nil
}

func prepareText(p *api.StrategyParams) error {
	if p.MaxLength == nil {
		p.MaxLength = new(int32(255))
	}
	if p.Sentences == nil {
		p.Sentences = new(int32(1))
	}
	if *p.MaxLength < 1 || *p.MaxLength > 4096 || *p.Sentences < 1 || *p.Sentences > 100 {
		return errors.New("text maxLength must be 1..4096 and sentences 1..100")
	}
	return nil
}

func (c *Column) prepareConstant(resolvedConstant *string) error {
	p := &c.params
	if (p.Value == nil) == (p.ValueFrom == nil) {
		return errors.New("constant requires exactly one value or valueFrom")
	}
	if p.ValueFrom != nil && (p.ValueFrom.Name == "" || p.ValueFrom.Key == "") {
		return errors.New("constant valueFrom requires Secret name and key")
	}
	if p.Value != nil {
		c.constant = new(*p.Value)
	} else if resolvedConstant != nil {
		c.constant = new(*resolvedConstant)
	}
	return nil
}

func prepareHash(p *api.StrategyParams) error {
	if p.Encoding == "" {
		p.Encoding = "hex"
	}
	limit := int32(64)
	if p.Encoding == "base64" {
		limit = 44
	} else if p.Encoding != "hex" {
		return errors.New("hash encoding must be hex or base64")
	}
	if p.Length == nil {
		p.Length = new(limit)
	}
	if *p.Length > limit {
		return fmt.Errorf("hash length exceeds %s output length %d", p.Encoding, limit)
	}
	return nil
}

func prepareMask(p *api.StrategyParams) error {
	if p.KeepPrefix == nil {
		p.KeepPrefix = new(int32(0))
	}
	if p.KeepSuffix == nil {
		p.KeepSuffix = new(int32(0))
	}
	if *p.KeepPrefix < 0 || *p.KeepSuffix < 0 || *p.KeepPrefix > 4096 || *p.KeepSuffix > 4096 {
		return errors.New("mask prefix and suffix must be between 0 and 4096")
	}
	if p.MaskChar == "" {
		p.MaskChar = "*"
	}
	if !utf8.ValidString(p.MaskChar) || utf8.RuneCountInString(p.MaskChar) != 1 {
		return errors.New("maskChar must be one Unicode character")
	}
	return nil
}
