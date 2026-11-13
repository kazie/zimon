# zimon list: what you have and whether it is up to date.
Source docs/tapes/common/settings.tape
Output docs/img/list.gif
Set Height 400

Hide
Type "source docs/tapes/demo-env.sh wikipedia_en_100 devdocs_en_go unix.stackexchange.com_en_all wikivoyage_en_all:nopic" Enter
Sleep 4s
Type "bump wikipedia_en_100 && zimon refresh >/dev/null 2>&1 && clear" Enter
Sleep 1s
Show

Type "zimon list" Sleep 400ms Enter
Sleep 3s
Screenshot docs/img/list.png
Sleep 500ms
