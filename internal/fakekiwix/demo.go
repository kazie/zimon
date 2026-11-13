package fakekiwix

// Sizes in bytes, in powers of 1000 as Kiwix lists them.
const (
	kB = 1000
	MB = 1000 * kB
	GB = 1000 * MB
)

// DemoBooks is a realistic-looking catalog for demos. Big entries advertise
// their real-world size but serve a small file; the ones the tapes download
// are small for real.
func DemoBooks() []Book {
	wp := func(lang, title, summary, flavour, date string, listed, articles, media int64) Book {
		return Book{
			Name: "wikipedia_" + lang + "_all", Flavour: flavour, Title: title, Summary: summary,
			Language: iso3[lang], Category: "wikipedia", Creator: "Wikipedia", Publisher: "openZIM",
			Tags: "wikipedia;_category:wikipedia;_pictures:" + yes(flavour != "nopic") + ";_videos:no;_details:" + yes(flavour != "mini") + ";_ftindex:yes",
			Date: date, Size: 3 * MB, ListedSize: listed, ArticleCount: articles, MediaCount: media,
		}
	}
	se := func(site, title, summary string, size int64, articles int64) Book {
		return Book{
			Name: site + "_en_all", Flavour: "", Title: title, Summary: summary, Language: "eng",
			Category: "stack_exchange", Creator: "Stack Exchange", Publisher: "openZIM",
			Tags: "stack_exchange;_category:stack_exchange;_pictures:yes;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-10-19", Size: size, ArticleCount: articles, MediaCount: articles / 12,
		}
	}
	return []Book{
		wp("en", "Wikipedia", "The free encyclopedia", "maxi", "2026-08-25", 127*GB, 21673012, 8469385),
		wp("en", "Wikipedia", "The free encyclopedia", "nopic", "2026-06-17", 52690707456, 19191219, 515775),
		wp("en", "Wikipedia", "The free encyclopedia", "mini", "2026-09-09", 14386799616, 18095848, 33992),
		wp("sv", "Wikipedia", "Den fria encyklopedin", "maxi", "2026-09-02", 41*GB, 7712004, 1903311),
		wp("sv", "Wikipedia", "Den fria encyklopedin", "nopic", "2026-09-02", 9200*MB, 7712004, 41022),
		wp("fr", "Wikipédia", "L’encyclopédie libre", "maxi", "2026-08-30", 39*GB, 7203554, 2210420),
		wp("de", "Wikipedia", "Die freie Enzyklopädie", "nopic", "2026-09-12", 18*GB, 6122940, 102874),
		{
			Name: "wikipedia_en_100", Title: "Wikipedia 100", Summary: "The top 100 Wikipedia articles",
			Language: "eng", Category: "wikipedia", Creator: "Wikipedia", Publisher: "openZIM",
			Tags: "wikipedia;_category:wikipedia;_pictures:yes;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-08-20", Size: 24 * MB, ArticleCount: 100, MediaCount: 1650,
		},
		{
			Name: "wikipedia_en-simple_all", Flavour: "mini", Title: "Wikipedia in simple English",
			Summary: "Simple English Wikipedia, intros only", Language: "eng", Category: "wikipedia",
			Creator: "Wikipedia", Publisher: "openZIM",
			Tags: "wikipedia;_category:wikipedia;_pictures:no;_videos:no;_details:no;_ftindex:yes",
			Date: "2026-09-27", Size: 18 * MB, ListedSize: 530 * MB, ArticleCount: 265120, MediaCount: 12,
		},
		{
			Name: "wiktionary_en_all", Flavour: "nopic", Title: "Wiktionary", Summary: "The free dictionary",
			Language: "eng", Category: "wiktionary", Creator: "Wiktionary", Publisher: "openZIM",
			Tags: "wiktionary;_category:wiktionary;_pictures:no;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-09-15", Size: 3 * MB, ListedSize: 8700 * MB, ArticleCount: 9800331, MediaCount: 211,
		},
		{
			Name: "wikivoyage_en_all", Flavour: "maxi", Title: "Wikivoyage",
			Summary: "The free worldwide travel guide", Language: "eng", Category: "wikivoyage",
			Creator: "Wikivoyage", Publisher: "openZIM",
			Tags: "wikivoyage;_category:wikivoyage;_pictures:yes;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-10-01", Size: 26 * MB, ListedSize: 1020 * MB, ArticleCount: 33440, MediaCount: 91233,
		},
		{
			Name: "wikivoyage_en_all", Flavour: "nopic", Title: "Wikivoyage",
			Summary: "The free worldwide travel guide", Language: "eng", Category: "wikivoyage",
			Creator: "Wikivoyage", Publisher: "openZIM",
			Tags: "wikivoyage;_category:wikivoyage;_pictures:no;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-10-01", Size: 12 * MB, ListedSize: 233 * MB, ArticleCount: 33440, MediaCount: 102,
		},
		se("superuser.com", "Super User", "Q&A for computer enthusiasts and power users", 3*MB, 512033),
		se("unix.stackexchange.com", "Unix & Linux", "Q&A for users of Linux, FreeBSD and other Un*x-like systems", 14*MB, 268004),
		se("askubuntu.com", "Ask Ubuntu", "Q&A for Ubuntu users and developers", 3*MB, 461201),
		se("cooking.stackexchange.com", "Seasoned Advice", "Q&A for professional and amateur chefs", 9*MB, 30511),
		se("english.stackexchange.com", "English Language & Usage", "Q&A for linguists, etymologists, and serious English language enthusiasts", 3*MB, 237615),
		{
			Name: "gutenberg_en_all", Title: "Project Gutenberg Library", Summary: "The first producer of free eBooks",
			Language: "eng", Category: "gutenberg", Creator: "gutenberg.org", Publisher: "openZIM",
			Tags: "gutenberg;_category:gutenberg;_pictures:yes;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-07-14", Size: 3 * MB, ListedSize: 206 * GB, ArticleCount: 74120, MediaCount: 120994,
		},
		{
			Name: "ifixit_en_all", Title: "iFixit", Summary: "The free repair guide for everything",
			Language: "eng", Category: "iFixit", Creator: "iFixit", Publisher: "openZIM",
			Tags: "ifixit;_category:iFixit;_pictures:yes;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-06-30", Size: 3 * MB, ListedSize: 3400 * MB, ArticleCount: 92341, MediaCount: 410233,
		},
		{
			Name: "devdocs_en_go", Title: "Go Docs", Summary: "Go documentation, by DevDocs",
			Language: "eng", Category: "devdocs", Creator: "DevDocs", Publisher: "openZIM",
			Tags: "devdocs;_category:devdocs;_pictures:no;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-10-12", Size: 6 * MB, ArticleCount: 2210, MediaCount: 4,
		},
		{
			Name: "devdocs_en_python", Title: "Python Docs", Summary: "Python 3 documentation, by DevDocs",
			Language: "eng", Category: "devdocs", Creator: "DevDocs", Publisher: "openZIM",
			Tags: "devdocs;_category:devdocs;_pictures:no;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-10-12", Size: 8 * MB, ArticleCount: 3104, MediaCount: 6,
		},
		{
			Name: "archlinux_en_all", Flavour: "maxi", Title: "ArchWiki", Summary: "The Arch Linux wiki",
			Language: "eng", Category: "other", Creator: "Arch Linux", Publisher: "openZIM",
			Tags: "archlinux;_category:other;_pictures:yes;_videos:no;_details:yes;_ftindex:yes",
			Date: "2026-10-05", Size: 21 * MB, ListedSize: 31 * MB, ArticleCount: 4211, MediaCount: 610,
		},
		{
			Name: "phet_en_all", Title: "PhET Interactive Simulations", Summary: "Free science and math simulations",
			Language: "eng", Category: "phet", Creator: "PhET", Publisher: "openZIM",
			Tags: "phet;_category:phet;_pictures:yes;_videos:no;_details:yes;_ftindex:no",
			Date: "2026-05-18", Size: 3 * MB, ListedSize: 81 * MB, ArticleCount: 154, MediaCount: 912,
		},
		{
			Name: "ted_mul_climate", Title: "TED Climate", Summary: "TED talks about climate change",
			Language: "eng,fra,spa", Category: "ted", Creator: "TED", Publisher: "openZIM",
			Tags: "ted;_category:ted;_pictures:yes;_videos:yes;_details:yes;_ftindex:yes",
			Date: "2026-04-02", Size: 3 * MB, ListedSize: 1500 * MB, ArticleCount: 88, MediaCount: 260,
		},

		// Enough of @linux, @anime-manga and @japan for the collections tape.
		other("www.mankier.com_en_all", "", "ManKier", "Linux man pages", "eng", 183*MB),
		other("devdocs_en_bash", "", "Bash Docs", "Bash documentation, by DevDocs", "eng", 2*MB),
		other("devdocs_en_git", "", "Git Docs", "Git documentation, by DevDocs", "eng", 3*MB),
		other("devdocs_en_nix", "", "Nix Docs", "Nix documentation, by DevDocs", "eng", 2*MB),
		other("gentoo_en_all", "maxi", "Gentoo Wiki", "The Gentoo Linux wiki", "eng", 96*MB),
		se("serverfault.com", "Server Fault", "Q&A for system and network administrators", 3*MB, 315022),
		se("anime.stackexchange.com", "Anime & Manga", "Q&A for anime and manga fans", 3*MB, 21044),
		other("allthetropes_en_all", "maxi", "All The Tropes", "Everything about fiction you never wanted to know", "eng", 1481*MB),
		other("evageeks_en_all", "maxi", "EvaWiki", "An encyclopedia of Evangelion", "eng", 18*MB),
		other("bulbagarden_en_all", "nopic", "Bulbapedia", "The community-driven Pokémon encyclopedia", "eng", 424*MB),
		other("pokemonwiki_en_all", "maxi", "Pokémon Wiki", "Encyclopedia of the Pokémon franchise", "eng", 847*MB),
		other("wiktionary_ja_all", "nopic", "ウィクショナリー", "日本語版ウィクショナリー", "jpn", 613*MB),
		other("japanese.stackexchange.com_mul_all", "", "Japanese Language", "Q&A for students, teachers and linguists of Japanese", "eng,jpn", 183*MB),
		other("wikipedia_ja_top", "mini", "ウィキペディアのベスト", "日本語版ウィキペディアの最高の記事", "jpn", 191*MB),
		other("wikipedia_ja_top", "nopic", "ウィキペディアのベスト", "日本語版ウィキペディアの最高の記事", "jpn", 1931*MB),
		other("wikipedia_ja_top", "maxi", "ウィキペディアのベスト", "日本語版ウィキペディアの最高の記事", "jpn", 4216*MB),
		other("wikivoyage_ja_all", "nopic", "ウィキボヤージュ", "フリーの旅行ガイド", "jpn", 20*MB),
		other("wikibooks_ja_all", "nopic", "ウィキブックス", "日本語版ウィキブックス", "jpn", 103*MB),
		other("wikisource_ja_all", "nopic", "ウィキソース", "日本語版ウィキソース", "jpn", 248*MB),
		other("wikipedia_ja_medicine", "nopic", "医療ウィキペディア", "医療記事", "jpn", 178*MB),
		other("wikipedia_ja_all", "mini", "ウィキペディア", "日本語版ウィキペディア", "jpn", 3311*MB),
		other("wikipedia_ja_all", "nopic", "ウィキペディア", "日本語版ウィキペディア", "jpn", 15849*MB),
		other("wikipedia_ja_all", "maxi", "ウィキペディア", "日本語版ウィキペディア", "jpn", 30763*MB),
		other("granbluefantasy_en_all", "nopic", "Granblue Fantasy Wiki", "The Granblue Fantasy wiki", "eng", 53*MB),
		other("wikipedia_en_comics", "nopic", "Comics by Wikipedia", "Wikipedia articles on comics", "eng", 327*MB),
	}
}

// other is a small demo book that advertises listed bytes in the catalog.
func other(name, flavour, title, summary, lang string, listed int64) Book {
	return Book{
		Name: name, Flavour: flavour, Title: title, Summary: summary, Language: lang,
		Creator: "openZIM", Publisher: "openZIM", Tags: "_pictures:" + yes(flavour != "nopic" && flavour != "mini") + ";_ftindex:yes",
		Date: "2026-10-20", Size: 2 * MB, ListedSize: listed, ArticleCount: 10000 + listed/100000, MediaCount: listed / 1000000,
	}
}

var iso3 = map[string]string{"en": "eng", "sv": "swe", "fr": "fra", "de": "deu"}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
