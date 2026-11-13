# zimon update: fetch newer releases, verify, swap, clean up.
Source docs/tapes/common/settings.tape
Output docs/img/update.gif
Set Height 480

Hide
Type "source docs/tapes/demo-env.sh wikipedia_en_100 devdocs_en_go archlinux_en_all:maxi" Enter
Sleep 4s
Type "bump wikipedia_en_100 && bump archlinux_en_all:maxi && clear" Enter
Sleep 1s
Show

Type "zimon update --check" Sleep 400ms Enter
Sleep 2.5s
Type "zimon update" Sleep 400ms Enter
Sleep 1.5s
Type "y" Sleep 300ms Enter
Sleep 9s
Screenshot docs/img/update.png
Sleep 500ms
Type "zimon list" Sleep 400ms Enter
Sleep 3s
