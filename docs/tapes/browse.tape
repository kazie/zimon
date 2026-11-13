# zimon browse: filter, switch category, download in the background.
Source docs/tapes/common/settings.tape
# live: a download runs behind these screens, so their timing varies
Output docs/img/browse.gif
Set Height 760

Hide
Type "source docs/tapes/demo-env.sh wikipedia_en_100 devdocs_en_go" Enter
Sleep 3s
Show

Type "zimon browse" Sleep 400ms Enter
Sleep 2s
Down 3 Sleep 1s
Type "/" Sleep 400ms
Type "stack" Sleep 1.2s
Enter Sleep 400ms
Down 2 Sleep 600ms
Type "a" Sleep 1.2s
Screenshot docs/img/browse.png
Sleep 2.5s
Escape Sleep 600ms
Tab Sleep 900ms
Tab Sleep 900ms
Tab Sleep 1.2s
Type "l" Sleep 1.5s
Type "l" Sleep 800ms
Shift+Tab Sleep 400ms Shift+Tab Sleep 400ms Shift+Tab Sleep 1s
Type "c" Sleep 1.5s
Down 14 Sleep 1s
Type "1" Sleep 1.5s
Type "y" Sleep 1.5s
Screenshot docs/img/browse-collections.png
Sleep 6s
Type "q" Sleep 500ms
Type "y" Sleep 500ms
