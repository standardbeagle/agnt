// Shared audit utilities

(function() {
  'use strict';

  function truncateString(str, maxLength) {
    if (!str || typeof str !== 'string') return str;
    if (str.length <= maxLength) return str;
    return str.substring(0, maxLength) + '...';
  }

  function truncateUrl(url, maxLength) {
    if (!url || typeof url !== 'string') return url;
    if (url.length <= maxLength) return url;
    // Keep protocol + domain + last part of path
    try {
      var u = new URL(url);
      var base = u.protocol + '//' + u.host;
      var remaining = maxLength - base.length - 4; // 4 for "..."
      if (remaining > 10) {
        return base + '/...' + u.pathname.slice(-remaining);
      }
      return base + '/...';
    } catch (e) {
      return truncateString(url, maxLength);
    }
  }

  window.__devtool_audit_utils = {
    truncateString: truncateString,
    truncateUrl: truncateUrl
  };
})();
