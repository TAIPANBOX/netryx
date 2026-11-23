'use strict';
'require view';
'require rpc';

// LuCI's "Network console" entry: asks the router for a one-time code for
// the signed-in user and opens the netryx console with it, so there is no
// second login. The code rides in the address fragment, which is never sent
// to a server, and the console takes it out of the address and the history
// at once. It is good for 30 seconds and one use, and only in this browser:
// the page first puts a random nonce in a cookie for the router's host (a
// cookie is shared by the ports of one host), and the code opens only
// together with it, so a link with a code opens nothing in anyone else's
// browser. Strings go in as text nodes only.

var callHandoff = rpc.declare({ object: 'luci.netryx', method: 'handoff', params: [ 'nonce' ] });

function newNonce() {
	var b = new Uint8Array(16);
	window.crypto.getRandomValues(b);
	return Array.prototype.map.call(b, function(x) { return ('0' + x.toString(16)).slice(-2); }).join('');
}

function hostFor(h) {
	return h.indexOf(':') >= 0 ? '[' + h + ']' : h;
}

return view.extend({
	load: function() {
		var nonce = newNonce();
		document.cookie = 'netryx_handoff=' + nonce + '; Path=/; Max-Age=60; SameSite=Strict' +
			(window.location.protocol == 'https:' ? '; Secure' : '');
		return callHandoff(nonce).catch(function(e) { return { error: String(e) }; });
	},

	render: function(r) {
		var title = E('h2', {}, _('Network console'));
		if (!r || r.error) {
			return E('div', {}, [ title, E('p', { 'class': 'alert-message warning' },
				[ _('The console cannot be opened: %s').format((r && r.error) || _('no answer from the router')) ]) ]);
		}
		var base = r.scheme + '://' + hostFor(window.location.hostname) + ':' + r.port + '/';
		if (r.loopback) {
			return E('div', {}, [ title,
				E('p', {}, [ _('The console listens on the router itself only (option listen in /etc/config/netryx). Reach it through an SSH tunnel:') ]),
				E('pre', {}, [ 'ssh -L ' + r.port + ':127.0.0.1:' + r.port + ' root@' + window.location.hostname ]),
				E('p', {}, [ _('then open http://127.0.0.1:%d/ on your computer. To open it from here instead, set listen to the router\'s address on the house network and restart netryx.').format(r.port) ]) ]);
		}
		window.location.replace(base + '#luci=' + r.code);
		return E('div', {}, [ title,
			E('p', {}, [ _('Opening the network console...') ]),
			r.scheme == 'https' ? E('p', { 'class': 'cbi-section-descr' },
				[ _('The console uses a certificate of the router\'s own. If the browser warns about it, accept it once for %s, then open this page again. Signing out of the console leaves you signed in here.').format(base) ]) : '' ]);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
