# zimon collections: curated sets, depth levels and library.toml.
Source docs/tapes/common/settings.tape
Output docs/img/collections.gif
Set Height 820

Hide
Type "source docs/tapes/demo-env.sh" Enter
Sleep 3s
Show

Type "zimon collections" Sleep 400ms Enter
Sleep 2.5s
Screenshot docs/img/collections.png
Sleep 500ms
Type "clear" Enter

Type "zimon collections show anime-manga" Sleep 400ms Enter
Sleep 2.5s
Screenshot docs/img/collections-show.png
Sleep 500ms
Type "clear" Enter

Type "zimon add @japan" Sleep 400ms Enter
Sleep 2s
Type "1" Sleep 600ms Enter
Sleep 2s
Enter
Sleep 5s
Type "clear" Enter

Type "zimon level @japan standard" Sleep 400ms Enter
Sleep 2s
Screenshot docs/img/collections-level.png
Sleep 500ms
Type "y" Sleep 400ms Enter
Sleep 6s
Type "clear" Enter

Type "zimon collections" Sleep 400ms Enter
Sleep 2.5s
Type "clear" Enter

Type "cat ~/.config/zimon/library.toml" Sleep 400ms Enter
Sleep 3s
Screenshot docs/img/library-toml.png
Sleep 500ms
Type "clear" Enter
Type "zimon budget" Sleep 400ms Enter
Sleep 3s
