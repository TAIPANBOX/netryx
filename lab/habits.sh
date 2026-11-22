#!/bin/sh
# One fakehouse device's habits, run inside its network namespace by
# fakehouse.sh: `ip netns exec fh-<name> sh habits.sh <name>`.
set -u
# A device's habits. Each runs forever inside its namespace; `down` kills it.
case "${1:?device name}" in
	tv) while :; do
		curl -s -o /dev/null --limit-rate 2M "https://speed.cloudflare.com/__down?bytes=20000000"
		curl -s -o /dev/null https://www.youtube.com/ https://www.netflix.com/ https://i.ytimg.com/
		sleep 90
	done ;;
	laptop) while :; do
		for u in https://www.bbc.co.uk/ https://github.com/ https://en.wikipedia.org/ https://www.anthropic.com/ \
			https://zoom.us/ https://docs.google.com/ https://outlook.office.com/ https://stackoverflow.com/; do
			curl -s -o /dev/null -m 15 "$u"
			sleep 8
		done
		curl -s -o /dev/null --limit-rate 1M "https://speed.cloudflare.com/__down?bytes=8000000"
	done ;;
	phone) while :; do
		for u in https://www.instagram.com/ https://www.whatsapp.com/ https://api.telegram.org/ \
			https://www.apple.com/ https://www.icloud.com/ https://open.spotify.com/ https://www.tiktok.com/; do
			curl -s -o /dev/null -m 15 "$u"
			sleep 12
		done
	done ;;
	camera) while :; do
		# A camera mostly uploads: the asymmetry is what an exfiltration
		# detector will later look for, so the lab must have it.
		head -c 400000 /dev/urandom | curl -s -o /dev/null -m 30 --data-binary @- https://speed.cloudflare.com/__up
		sleep 30
	done ;;
	console) while :; do
		curl -s -o /dev/null -m 15 https://www.xbox.com/ https://store.steampowered.com/
		curl -s -o /dev/null --limit-rate 1M "https://speed.cloudflare.com/__down?bytes=10000000"
		sleep 120
	done ;;
	esac
