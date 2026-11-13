package catalog

import "strings"

// Features turns Kiwix's internal tags (_pictures:no, _ftindex:yes, ...) into words.
func Features(tags string) (feats []string, plain []string) {
	for t := range strings.SplitSeq(tags, ";") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "_") {
			plain = append(plain, t)
			continue
		}
		k, v, _ := strings.Cut(strings.TrimPrefix(t, "_"), ":")
		switch k {
		case "pictures":
			feats = append(feats, yesNo(v, "pictures", "no pictures"))
		case "videos":
			feats = append(feats, yesNo(v, "videos", "no videos"))
		case "details":
			feats = append(feats, yesNo(v, "full articles", "intros only"))
		case "ftindex":
			feats = append(feats, yesNo(v, "full-text search", "no full-text search"))
		}
	}
	return feats, plain
}

func yesNo(v, yes, no string) string {
	if v == "no" {
		return no
	}
	return yes
}
