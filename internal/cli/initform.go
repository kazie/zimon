package cli

import (
	"errors"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/kazie/zimon/internal/config"
)

// initLanguages are offered by zimon init's form: languages with much in the
// Kiwix catalog (ISO 639-3, as Kiwix tags books). Others go in config.toml.
var initLanguages = []struct{ code, name string }{
	{"ara", "Arabic"}, {"ben", "Bengali"}, {"cat", "Catalan"}, {"zho", "Chinese"}, {"ces", "Czech"},
	{"dan", "Danish"}, {"nld", "Dutch"}, {"eng", "English"}, {"fin", "Finnish"}, {"fra", "French"},
	{"deu", "German"}, {"ell", "Greek"}, {"heb", "Hebrew"}, {"hin", "Hindi"}, {"hun", "Hungarian"},
	{"ind", "Indonesian"}, {"ita", "Italian"}, {"jpn", "Japanese"}, {"kor", "Korean"}, {"fas", "Persian"},
	{"pol", "Polish"}, {"por", "Portuguese"}, {"ron", "Romanian"}, {"rus", "Russian"}, {"spa", "Spanish"},
	{"swe", "Swedish"}, {"tha", "Thai"}, {"tur", "Turkish"}, {"ukr", "Ukrainian"}, {"vie", "Vietnamese"},
}

// initForm asks zimon init's questions on a terminal. dir and langs hold the
// defaults and get the answers; no languages means all of them.
func initForm(dir *string, langs *[]string) *huh.Form {
	var opts []huh.Option[string]
	for _, l := range initLanguages {
		opts = append(opts, huh.NewOption(l.name+" ("+l.code+")", l.code))
	}
	// Keep languages already set that the list lacks.
	for _, code := range *langs {
		if !slices.ContainsFunc(initLanguages, func(l struct{ code, name string }) bool { return l.code == code }) {
			opts = append(opts, huh.NewOption(code, code))
		}
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Where should ZIM files live?").
			Value(dir).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("say where, e.g. ~/zim")
				}
				_, err := config.ExpandHome(s)
				return err
			}),
		huh.NewMultiSelect[string]().
			Title("Languages to show by default").
			// Picks can be below the visible part of the list, so name them.
			DescriptionFunc(func() string { return pickedLanguages(*langs) }, langs).
			Options(opts...).
			Value(langs).
			Height(12),
	)).WithTheme(huh.ThemeFunc(huh.ThemeBase16))
}

// pickedLanguages describes the languages picked so far, by name.
func pickedLanguages(codes []string) string {
	if len(codes) == 0 {
		return "None picked: all languages. Others: zimon config edit."
	}
	names := make([]string, len(codes))
	for i, code := range codes {
		names[i] = code
		for _, l := range initLanguages {
			if l.code == code {
				names[i] = l.name
			}
		}
	}
	return "Picked: " + strings.Join(names, ", ") + ". Others: zimon config edit."
}
