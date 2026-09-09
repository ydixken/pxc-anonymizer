// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package strategy

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

const MaxUniqueAttempts = 8

const caseUpper = "Upper"

var names = []string{
	"firstName", "lastName", "fullName", "jobTitle", "email", "phone", "date", "address",
	"streetAddress", "streetSuffix", "city", "postalCode", "state", "country", "company",
	"companySuffix", "vatId", "iban", "ipv4Public", "url", "uuid", "alphanumeric", "digits",
	"number", "text", "constant", "hash", "mask", "null",
}

var legacy = map[string]api.Strategy{
	"first_name": api.StrategyFirstName, "last_name": api.StrategyLastName,
	"name": api.StrategyFullName, "job": api.StrategyJobTitle,
	"ascii_email": api.StrategyEmail, "email": api.StrategyEmail,
	"phone_number": api.StrategyPhone, "date": api.StrategyDate,
	"address": api.StrategyAddress, "street_address": api.StrategyStreetAddress,
	"street_suffix_long": api.StrategyStreetSuffix, "city": api.StrategyCity,
	"postalcode": api.StrategyPostalCode, "state": api.StrategyState,
	"country": api.StrategyCountry, "company": api.StrategyCompany,
	"company_suffix": api.StrategyCompanySuffix, "vat_id": api.StrategyVATID,
	"ipv4_public": api.StrategyIPv4Public, "url": api.StrategyURL,
	"uuid": api.StrategyUUID, "uuid4": api.StrategyUUID,
	"alphanumeric": api.StrategyAlphanumeric, "alphanumeric_upper_case": api.StrategyAlphanumeric,
	"random_number": api.StrategyDigits, "null": api.StrategyNull,
}

var callPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(?:\(([0-9]+)\))?$`)

func Names() []string {
	result := slices.Clone(names)
	slices.Sort(result)
	return result
}

// Coverage returns a copy so callers cannot alter the accepted V1 mapping.
func Coverage() map[string]api.Strategy {
	return maps.Clone(legacy)
}

// Parse is the explicit V1 migration boundary; policy rules use canonical names only.
func Parse(expression string) (api.Strategy, *api.StrategyParams, error) {
	parts := callPattern.FindStringSubmatch(expression)
	if parts == nil {
		return "", nil, unknown(expression)
	}
	name := api.Strategy(parts[1])
	if resolved, found := legacy[parts[1]]; found {
		name = resolved
	}
	if !slices.Contains(names, string(name)) {
		return "", nil, unknown(expression)
	}
	params := &api.StrategyParams{}
	if parts[1] == "alphanumeric_upper_case" {
		params.Case = caseUpper
	}
	if parts[2] != "" {
		if name != api.StrategyAlphanumeric && name != api.StrategyDigits {
			return "", nil, fmt.Errorf("strategy %q does not accept a positional length", name)
		}
		length, err := strconv.ParseInt(parts[2], 10, 32)
		if err != nil || length < 1 || length > 4096 {
			return "", nil, fmt.Errorf("strategy length must be between 1 and 4096")
		}
		params.Length = new(int32(length))
	}
	return name, params, nil
}

func unknown(name string) error {
	return fmt.Errorf("unknown strategy %q; valid strategies: %s", name, strings.Join(Names(), ", "))
}

func consistentDefault(name api.Strategy) bool {
	switch name {
	case api.StrategyAlphanumeric, api.StrategyDigits, api.StrategyNumber, api.StrategyText,
		api.StrategyConstant, api.StrategyMask, api.StrategyNull:
		return false
	default:
		return true
	}
}
