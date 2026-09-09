// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package strategy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/brianvoe/gofakeit/v7"
	"golang.org/x/text/unicode/norm"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
)

type Row struct {
	Database   string
	Table      string
	PrimaryKey string
	Column     string
}

type Generator struct {
	key   [32]byte
	today time.Time
}

var policyHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func New(seed []byte, policyHash string, today time.Time) (*Generator, error) {
	if len(seed) < 32 {
		return nil, errors.New("determinism seed must contain at least 32 bytes")
	}
	if !policyHashPattern.MatchString(policyHash) {
		return nil, errors.New("policy hash must be a SHA-256 digest")
	}
	if today.IsZero() {
		return nil, errors.New("date reference time is required")
	}
	mac := hmac.New(sha256.New, seed)
	_, _ = mac.Write([]byte("pxc-anonymizer/v1/" + policyHash))
	return &Generator{key: [32]byte(mac.Sum(nil)), today: today.UTC().Truncate(24 * time.Hour)}, nil
}

// Each call owns its RNG so worker scheduling cannot change pseudonyms.
func (g *Generator) Generate(column *Column, input any, row Row, retry uint8) (any, error) {
	if column == nil {
		return nil, errors.New("compiled column is required")
	}
	if retry >= MaxUniqueAttempts {
		return nil, errors.New("UNIQUE retry counter exceeds eight attempts")
	}
	if input == nil && column.onNull != api.ValueHandlingGenerate {
		return nil, nil
	}
	value := inputString(input)
	if input != nil && value == "" && column.onEmpty != api.ValueHandlingGenerate {
		return "", nil
	}
	name, p := column.rule.Strategy, column.params
	switch name {
	case api.StrategyNull:
		return nil, nil
	case api.StrategyConstant:
		if column.constant == nil {
			return nil, errors.New("constant Secret value has not been resolved from its projected file")
		}
		return *column.constant, nil
	case api.StrategyMask:
		runes := []rune(value)
		prefix, suffix := min(int(*p.KeepPrefix), len(runes)), min(int(*p.KeepSuffix), len(runes))
		if prefix+suffix >= len(runes) {
			return value, nil
		}
		return string(runes[:prefix]) + strings.Repeat(p.MaskChar, len(runes)-prefix-suffix) + string(runes[len(runes)-suffix:]), nil
	}
	h, err := g.valueHash(column, value, row, retry)
	if err != nil {
		return nil, err
	}
	source := rand.NewPCG(binary.LittleEndian.Uint64(h[:8]), binary.LittleEndian.Uint64(h[8:16]))
	faker := gofakeit.NewFaker(source, false)
	return g.generate(column.params, name, h, faker, source)
}

var fakerStrategies = map[api.Strategy]func(*gofakeit.Faker) string{
	api.StrategyFirstName:     (*gofakeit.Faker).FirstName,
	api.StrategyLastName:      (*gofakeit.Faker).LastName,
	api.StrategyFullName:      (*gofakeit.Faker).Name,
	api.StrategyJobTitle:      (*gofakeit.Faker).JobTitle,
	api.StrategyStreetAddress: (*gofakeit.Faker).Street,
	api.StrategyStreetSuffix:  (*gofakeit.Faker).StreetSuffix,
	api.StrategyCity:          (*gofakeit.Faker).City,
	api.StrategyPostalCode:    (*gofakeit.Faker).Zip,
	api.StrategyState:         (*gofakeit.Faker).State,
	api.StrategyCountry:       (*gofakeit.Faker).Country,
	api.StrategyCompany:       (*gofakeit.Faker).Company,
	api.StrategyCompanySuffix: (*gofakeit.Faker).CompanySuffix,
	api.StrategyURL:           (*gofakeit.Faker).URL,
	api.StrategyUUID:          (*gofakeit.Faker).UUID,
}

func (g *Generator) generate(p api.StrategyParams, name api.Strategy, h [32]byte, faker *gofakeit.Faker, source *rand.PCG) (any, error) {
	if generate, found := fakerStrategies[name]; found {
		return generate(faker), nil
	}
	switch name {
	case api.StrategyEmail:
		parts := strings.SplitN(faker.Email(), "@", 2)
		domain := p.Domain
		if domain == "" {
			domain = parts[1]
		}
		local := strings.Map(func(r rune) rune {
			if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_') {
				return unicode.ToLower(r)
			}
			return -1
		}, parts[0])
		local = strings.Trim(local, ".")
		if local == "" {
			local = "anonymous"
		}
		return local[:min(len(local), 51)] + "-" + hex.EncodeToString(h[16:22]) + "@" + domain, nil
	case api.StrategyPhone:
		return decimal(h, int(*p.Digits)), nil
	case api.StrategyDate:
		from, _ := time.Parse(time.DateOnly, p.From)
		to := g.today
		if p.To != "" {
			to, _ = time.Parse(time.DateOnly, p.To)
		}
		if to.Before(from) {
			return nil, errors.New("date range ends before it starts")
		}
		date := faker.DateRange(from, to)
		if p.Format == "datetime" {
			return date.UTC().Format(time.DateTime), nil
		}
		return date.UTC().Format(time.DateOnly), nil
	case api.StrategyAddress:
		return faker.Street() + ", " + faker.City() + ", " + faker.State() + " " + faker.Zip(), nil
	case api.StrategyVATID:
		return p.Country + decimal(h, 9), nil
	case api.StrategyIBAN:
		bban := decimal(h, 18)
		value, _ := new(big.Int).SetString(bban+"131400", 10)
		check := 98 - new(big.Int).Mod(value, big.NewInt(97)).Int64()
		return fmt.Sprintf("DE%02d%s", check, bban), nil
	case api.StrategyIPv4Public:
		return publicIPv4(rand.New(source))
	case api.StrategyAlphanumeric:
		alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		switch p.Case {
		case caseUpper:
			alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		case "Lower":
			alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
		}
		rng := rand.New(source)
		result := make([]byte, int(*p.Length))
		for i := range result {
			result[i] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(result), nil
	case api.StrategyDigits:
		return decimal(h, int(*p.Length)), nil
	case api.StrategyNumber:
		minimum := big.NewInt(*p.Min)
		span := new(big.Int).Sub(big.NewInt(*p.Max), minimum)
		span.Add(span, big.NewInt(1))
		number := new(big.Int).Mod(new(big.Int).SetBytes(h[:]), span)
		return number.Add(number, minimum).Int64(), nil
	case api.StrategyText:
		sentences := make([]string, int(*p.Sentences))
		for i := range sentences {
			sentences[i] = faker.Sentence()
		}
		runes := []rune(strings.Join(sentences, " "))
		return string(runes[:min(len(runes), int(*p.MaxLength))]), nil
	case api.StrategyHash:
		result := hex.EncodeToString(h[:])
		if p.Encoding == "base64" {
			result = base64.StdEncoding.EncodeToString(h[:])
		}
		return result[:int(*p.Length)], nil
	default:
		return nil, unknown(string(name))
	}
}

func (g *Generator) valueHash(column *Column, value string, row Row, retry uint8) ([32]byte, error) {
	var payload []byte
	if column.consistent {
		value = norm.NFC.String(strings.TrimSpace(value))
		if column.rule.Strategy == api.StrategyEmail {
			value = strings.ToLower(value)
		}
		payload = []byte(string(column.rule.Strategy) + "\x1f" + value)
	} else {
		if row.Database == "" || row.Table == "" || row.PrimaryKey == "" || row.Column == "" {
			return [32]byte{}, errors.New("non-consistent generation requires database, table, primary key and column")
		}
		// JSON framing prevents ambiguous composite row identities without depending on SQL types.
		payload, _ = json.Marshal([]string{row.Database, row.Table, row.PrimaryKey, row.Column})
	}
	mac := hmac.New(sha256.New, g.key[:])
	_, _ = mac.Write(payload)
	h := [32]byte(mac.Sum(nil))
	if retry > 0 {
		// Hash the fixed-width base digest so input delimiters cannot impersonate a retry.
		mac.Reset()
		_, _ = mac.Write([]byte("pxc-anonymizer/unique-retry/"))
		_, _ = mac.Write(h[:])
		_, _ = mac.Write([]byte{retry})
		h = [32]byte(mac.Sum(nil))
	}
	return h, nil
}

func inputString(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(value)
	case string:
		return value
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(value)
	}
}

func decimal(h [32]byte, length int) string {
	modulus := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(length)), nil)
	result := new(big.Int).Mod(new(big.Int).SetBytes(h[:]), modulus).String()
	return strings.Repeat("0", length-len(result)) + result
}

var reservedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func publicIPv4(rng *rand.Rand) (string, error) {
	for range 128 {
		var raw [4]byte
		binary.BigEndian.PutUint32(raw[:], rng.Uint32())
		address := netip.AddrFrom4(raw)
		reserved := false
		for _, prefix := range reservedIPv4 {
			if prefix.Contains(address) {
				reserved = true
				break
			}
		}
		if !reserved {
			return address.String(), nil
		}
	}
	return "", errors.New("public IPv4 generation exhausted its bound")
}
