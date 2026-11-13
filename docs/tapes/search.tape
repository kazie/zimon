# zimon search: find ZIMs in the catalog.
Source docs/tapes/common/settings.tape
Output docs/img/search.gif
Set Height 560

Hide
Type "source docs/tapes/demo-env.sh wikipedia_en_100" Enter
Sleep 2s
Show

Type "zimon search wikipedia" Sleep 400ms Enter
Sleep 2.5s
Screenshot docs/img/search.png
Sleep 500ms
Type "zimon search stack exchange --max-size 5MB" Sleep 400ms Enter
Sleep 3s
