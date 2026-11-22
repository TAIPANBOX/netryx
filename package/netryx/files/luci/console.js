'use strict';
'require view';
'require rpc';

// LuCI's "Network console" entry: asks the router for a one-time code for
// the signed-in user and opens the netryx console with it, so there is no
// second login. The code rides in the address fragment, which is never sent
// to a server, and the console takes it out of the address and the history
// at once. It is good for 30 seconds and one use.

var callHandoff = rpc.declare({ object: 'luci.netryx', method: 'handoff' });

function hostFor(h) {
	return h.indexOf(':') >= 0 ? '[' + h + ']' : h;
}

return view.extend({
	load: function() {
		return callHandoff().catch(function(e) { return { error: String(e) }; });
	},

	render: function(r) {
		var title = E('h2', {}, _('Network console'));
		if (!r || r.error) {
			return E('div', {}, [ title, E('p', { 'class': 'alert-message warning' },
				_('The console cannot be opened: %s').format((r && r.error) || _('no answer from the router'))) ]);
		}
		var base = r.scheme + '://' + hostFor(window.location.hostname) + ':' + r.port + '/';
		if (r.loopback) {
			return E('div', {}, [ title,
				E('p', {}, _('The console listens on the router itself only (option listen in /etc/config/netryx). Reach it through an SSH tunnel:')),
				E('pre', {}, 'ssh -L ' + r.port + ':127.0.0.1:' + r.port + ' root@' + window.location.hostname),
				E('p', {}, _('then open http://127.0.0.1:%d/ on your computer. To open it from here instead, set listen to the router\'s address on the house network and restart netryx.').format(r.port)) ]);
		}
		window.location.replace(base + '#luci=' + r.code);
		return E('div', {}, [ title,
			E('p', {}, _('Opening the network console...')),
			r.scheme == 'https' ? E('p', { 'class': 'cbi-section-descr' },
				_('The console uses a certificate of the router\'s own. If the browser warns about it, accept it once for %s, then open this page again.').format(base)) : '' ]);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
