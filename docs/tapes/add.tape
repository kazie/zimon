# zimon add: pick a flavour, download with a progress bar, verify.
Source docs/tapes/common/settings.tape
Output docs/img/add.gif
Set Height 420

Hide
Type "source docs/tapes/demo-env.sh" Enter
Sleep 2s
Show

Type "zimon add wikivoyage_en_all" Sleep 400ms Enter
Sleep 1.5s
Type "1" Sleep 400ms Enter
Sleep 2.2s
# live: a download in progress, a little different on every render
Screenshot docs/img/add.png
Sleep 3.5s
Type "zimon add archlinux_en_all:maxi" Sleep 400ms Enter
Sleep 5s
