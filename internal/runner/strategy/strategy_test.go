// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package strategy

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"net/mail"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

const testValue = "value"
const testDigits = "digits"
const testEncodingBase64 = "base64"

const testHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

var testRow = Row{Database: "demo", Table: "people", PrimaryKey: `["tenant",42]`, Column: testValue}

func generator(t *testing.T, seed byte) *Generator {
	t.Helper()
	g, err := New(bytes.Repeat([]byte{seed}, 32), testHash, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func ruleFor(name string) api.ColumnRule {
	rule := api.ColumnRule{Name: testValue, Strategy: api.Strategy(name)}
	switch rule.Strategy {
	case api.StrategyAlphanumeric, api.StrategyDigits:
		rule.Params = &api.StrategyParams{Length: new(int32(16))}
	case api.StrategyConstant:
		rule.Params = &api.StrategyParams{Value: new("SANDBOX")}
	}
	return rule
}

func TestAllStrategiesDeterministicAcrossWorkers(t *testing.T) {
	if len(Names()) != 29 {
		t.Fatalf("registry must cover all 29 API strategies, got %d", len(Names()))
	}
	g := generator(t, 'A')
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			column, err := Compile(ruleFor(name), api.PolicyDefaults{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want, err := g.Generate(column, "original-value", testRow, 0)
			if err != nil {
				t.Fatal(err)
			}
			if name != "null" && fmt.Sprint(want) == "" {
				t.Fatal("generator returned an empty replacement")
			}
			var workers sync.WaitGroup
			for range 32 {
				workers.Go(func() {
					got, callErr := g.Generate(column, "original-value", testRow, 0)
					if callErr != nil || got != want {
						t.Errorf("concurrent output differs: error=%v", callErr)
					}
				})
			}
			workers.Wait()
		})
	}
}

func TestCompiledOutputLength(t *testing.T) {
	g := generator(t, 'A')
	for _, test := range []struct {
		name   api.Strategy
		params *api.StrategyParams
		length int
	}{
		{api.StrategyHash, nil, 64},
		{api.StrategyHash, &api.StrategyParams{Encoding: testEncodingBase64}, 44},
		{api.StrategyHash, &api.StrategyParams{Length: new(int32(12))}, 12},
		{api.StrategyDigits, &api.StrategyParams{Length: new(int32(8))}, 8},
		{api.StrategyAlphanumeric, &api.StrategyParams{Length: new(int32(7))}, 7},
		{api.StrategyPhone, nil, 15},
		{api.StrategyDate, nil, 10},
		{api.StrategyDate, &api.StrategyParams{Format: "datetime"}, 19},
		{api.StrategyUUID, nil, 36},
		{api.StrategyVATID, nil, 11},
		{api.StrategyIBAN, nil, 22},
		{api.StrategyEmail, nil, 0},
		{api.StrategyText, nil, 0},
	} {
		column, err := Compile(api.ColumnRule{Name: testValue, Strategy: test.name, Params: test.params}, api.PolicyDefaults{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		length, known := column.OutputLength()
		if known != (test.length != 0) || length != test.length {
			t.Fatalf("incorrect fixed output length for %s: %d, %t", test.name, length, known)
		}
		if known {
			value, err := g.Generate(column, "source", testRow, 0)
			if err != nil || utf8.RuneCountInString(fmt.Sprint(value)) != length {
				t.Fatalf("generated length differs for %s", test.name)
			}
		}
	}
}

func TestCoverageAndUnknownNames(t *testing.T) {
	for old, want := range Coverage() {
		expression := old
		if want == api.StrategyAlphanumeric || want == api.StrategyDigits {
			expression += "(5)"
		}
		got, params, err := Parse(expression)
		if err != nil || got != want {
			t.Fatalf("coverage %q: got %q, %v", expression, got, err)
		}
		if strings.Contains(expression, "(5)") && (params.Length == nil || *params.Length != 5) {
			t.Fatalf("length was not parsed for %s", expression)
		}
		if old == "alphanumeric_upper_case" && params.Case != "Upper" {
			t.Fatal("V1 uppercase semantics lost")
		}
	}
	for _, invalid := range []string{"random_letters", "typo", "alphanumeric(-1)", "alphanumeric(4097)", "email(5)"} {
		if _, _, err := Parse(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	_, _, err := Parse("random_letters")
	for _, name := range Names() {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("unknown error omits valid name %q", name)
		}
	}
	copyNames, copyCoverage := Names(), Coverage()
	copyNames[0], copyCoverage["first_name"] = "tampered", api.StrategyNull
	if slices.Contains(Names(), "tampered") || Coverage()["first_name"] != api.StrategyFirstName {
		t.Fatal("registry exposed mutable state")
	}
}

func TestHMACNormalizationAndIdentity(t *testing.T) {
	g := generator(t, 'A')
	column, err := Compile(ruleFor("hash"), api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "b63d04a81e4d68f734d5edad2a8b4a0a43df07a8df99b17d0df310104ff12b04"
	for _, input := range []any{"  Café  ", []byte("Cafe\u0301")} {
		got, callErr := g.Generate(column, input, Row{}, 0)
		if callErr != nil || got != want {
			t.Fatalf("HMAC/NFC vector differs: %v, %v", got, callErr)
		}
	}
	other, err := generator(t, 'B').Generate(column, "Café", Row{}, 0)
	if err != nil || other == want {
		t.Fatal("different seed did not isolate the pseudonym")
	}
	email, err := Compile(ruleFor("email"), api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	one, err := g.Generate(email, " PERSON@EXAMPLE.COM ", Row{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	two, err := g.Generate(email, "person@example.com", testRow, 0)
	if err != nil || one != two {
		t.Fatal("email normalization or cross-table consistency changed")
	}
	rowColumn, err := Compile(ruleFor(testDigits), api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := g.Generate(rowColumn, "first input", testRow, 0)
	after, _ := g.Generate(rowColumn, "changed input", testRow, 0)
	if before != after {
		t.Fatal("row-based strategy depends on already-anonymized input")
	}
	otherRow := testRow
	otherRow.PrimaryKey = `["tenant",43]`
	different, err := g.Generate(rowColumn, "first input", otherRow, 0)
	if err != nil || before == different {
		t.Fatal("different row identity reused output")
	}
	if _, err := g.Generate(rowColumn, "input", Row{}, 0); err == nil {
		t.Fatal("missing row identity was accepted")
	}
}

func TestGeneratedFormatsAndRetryBound(t *testing.T) {
	g := generator(t, 'A')
	for _, name := range []string{"email", "phone", "iban", "vatId", "ipv4Public", "uuid", "date", testDigits} {
		column, err := Compile(ruleFor(name), api.PolicyDefaults{EmailDomain: "example.invalid"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		output, err := g.Generate(column, "sensitive input", testRow, 0)
		if err != nil {
			t.Fatal(err)
		}
		value := output.(string)
		switch name {
		case "email":
			if _, err := mail.ParseAddress(value); err != nil || !regexp.MustCompile(`-[0-9a-f]{12}@example\.invalid$`).MatchString(value) {
				t.Fatalf("invalid anonymized email: %s", value)
			}
		case "phone", testDigits:
			length := map[string]int{string(api.StrategyPhone): 15, testDigits: 16}[name]
			if len(value) != length || strings.Trim(value, "0123456789") != "" {
				t.Fatalf("invalid %s shape", name)
			}
		case "iban":
			if len(value) != 22 || !strings.HasPrefix(value, "DE") {
				t.Fatal("invalid German IBAN shape")
			}
			number, ok := new(big.Int).SetString(value[4:]+"1314"+value[2:4], 10)
			if !ok || new(big.Int).Mod(number, big.NewInt(97)).Int64() != 1 {
				t.Fatal("IBAN fails mod-97")
			}
		case "vatId":
			if !regexp.MustCompile(`^DE[0-9]{9}$`).MatchString(value) {
				t.Fatal("invalid VAT shape")
			}
		case "ipv4Public":
			address, err := netip.ParseAddr(value)
			if err != nil || !address.IsGlobalUnicast() || address.IsPrivate() {
				t.Fatal("IPv4 is not public unicast")
			}
		case "uuid":
			if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(value) {
				t.Fatal("invalid UUIDv4")
			}
		case "date":
			if _, err := time.Parse(time.DateOnly, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	column, err := Compile(ruleFor(testDigits), api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[any]bool{}
	for retry := range MaxUniqueAttempts {
		value, err := g.Generate(column, "input", testRow, uint8(retry))
		if err != nil || outputs[value] {
			t.Fatalf("retry %d is not distinct: %v", retry, err)
		}
		outputs[value] = true
	}
	if _, err := g.Generate(column, "input", testRow, 8); err == nil {
		t.Fatal("accepted ninth UNIQUE attempt")
	}
}

func TestNullEmptyConstantsAndImmutability(t *testing.T) {
	g := generator(t, 'A')
	rule := ruleFor("constant")
	column, err := Compile(rule, api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	*rule.Params.Value = "changed"
	for _, input := range []any{nil, "", []byte{}} {
		value, err := g.Generate(column, input, Row{}, 0)
		if err != nil || (input == nil && value != nil) || (input != nil && value != "") {
			t.Fatal("default null/empty preservation changed")
		}
	}
	value, err := g.Generate(column, "input", Row{}, 0)
	if err != nil || value != "SANDBOX" {
		t.Fatal("compiled constant aliases caller memory")
	}
	ref := &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "sandbox"}, Key: testValue}
	secretRule := api.ColumnRule{Name: testValue, Strategy: api.StrategyConstant, OnNull: api.ValueHandlingGenerate, OnEmpty: api.ValueHandlingGenerate, Params: &api.StrategyParams{ValueFrom: ref}}
	unresolved, err := Compile(secretRule, api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate(unresolved, "input", Row{}, 0); err == nil {
		t.Fatal("unresolved Secret constant was accepted")
	}
	secret := "sandbox-only"
	resolved, err := Compile(secretRule, api.PolicyDefaults{}, &secret)
	if err != nil {
		t.Fatal(err)
	}
	secret = "changed"
	for _, input := range []any{nil, ""} {
		value, err := g.Generate(resolved, input, Row{}, 0)
		if err != nil || value != "sandbox-only" {
			t.Fatal("resolved constant or Generate override changed")
		}
	}
	mask, err := Compile(api.ColumnRule{Name: testValue, Strategy: api.StrategyMask, Params: &api.StrategyParams{KeepPrefix: new(int32(1)), KeepSuffix: new(int32(1)), MaskChar: "●"}}, api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	masked, err := g.Generate(mask, "éabc界", Row{}, 0)
	if err != nil || masked != "é●●●界" || !utf8.ValidString(masked.(string)) {
		t.Fatal("mask split a Unicode character")
	}
}

func TestParameterBoundaries(t *testing.T) {
	invalid := []api.ColumnRule{
		{Name: "x", Strategy: "random_letters"},
		{Name: "x", Strategy: api.StrategyDigits},
		{Name: "x", Strategy: api.StrategyDigits, Params: &api.StrategyParams{Length: new(int32(0))}},
		{Name: "x", Strategy: api.StrategyDigits, Params: &api.StrategyParams{Length: new(int32(4097))}},
		{Name: "x", Strategy: api.StrategyPhone, Params: &api.StrategyParams{Digits: new(int32(16))}},
		{Name: "x", Strategy: api.StrategyFirstName, Params: &api.StrategyParams{Locale: "de"}},
		{Name: "x", Strategy: api.StrategyIBAN, Params: &api.StrategyParams{Country: "GB"}},
		{Name: "x", Strategy: api.StrategyHash, Params: &api.StrategyParams{Encoding: "base64", Length: new(int32(45))}},
		{Name: "x", Strategy: api.StrategyDate, Params: &api.StrategyParams{From: "2026-01-01", To: "2025-01-01"}},
		{Name: "x", Strategy: api.StrategyNumber, Params: &api.StrategyParams{Min: new(int64(1)), Max: new(int64(0))}},
		{Name: "x", Strategy: api.StrategyConstant},
		{Name: "x", Strategy: api.StrategyNull, Consistent: new(false)},
		{Name: "x", Strategy: api.StrategyMask, Params: &api.StrategyParams{MaskChar: "ab"}},
		{Name: "x", Strategy: api.StrategyEmail, Params: &api.StrategyParams{Domain: "example.invalid\nBcc: bad"}},
	}
	for _, rule := range invalid {
		if err := Validate(rule, api.PolicyDefaults{}); err == nil {
			t.Fatalf("invalid %s parameters were accepted", rule.Strategy)
		}
	}
	g := generator(t, 'A')
	column, err := Compile(api.ColumnRule{Name: "x", Strategy: api.StrategyHash, Params: &api.StrategyParams{Encoding: "base64"}}, api.PolicyDefaults{Locale: "de"}, nil)
	if err != nil {
		t.Fatal("unrelated locale must not reject hash:", err)
	}
	value, err := g.Generate(column, "input", Row{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(value.(string))
	if err != nil || len(decoded) != 32 || len(value.(string)) != 44 {
		t.Fatal("base64 hash is not a complete SHA256 digest")
	}
	wide, err := Compile(api.ColumnRule{Name: "x", Strategy: api.StrategyNumber, Params: &api.StrategyParams{Min: new(int64(math.MinInt64)), Max: new(int64(math.MaxInt64))}}, api.PolicyDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate(wide, "input", testRow, 0); err != nil {
		t.Fatal("full int64 range overflowed:", err)
	}
	if _, err := New([]byte("short"), testHash, time.Now()); err == nil {
		t.Fatal("weak seed accepted")
	}
}

func TestEmailDomainASCII(t *testing.T) {
	const domain = "münchen.example"
	for _, ascii := range []*bool{nil, new(true), new(false)} {
		rule := api.ColumnRule{Name: string(api.StrategyEmail), Strategy: api.StrategyEmail, Params: &api.StrategyParams{Domain: domain, ASCII: ascii}}
		column, err := Compile(rule, api.PolicyDefaults{}, nil)
		if ascii == nil || *ascii {
			if err == nil {
				t.Fatal("default or explicit ASCII allowed a Unicode domain")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		value, err := generator(t, 'A').Generate(column, "input", testRow, 0)
		if err != nil || !strings.HasSuffix(value.(string), "@"+domain) {
			t.Fatal("explicit non-ASCII domain was not preserved")
		}
	}
}
