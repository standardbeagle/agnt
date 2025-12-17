// Content extraction and information architecture module
// Provides tools for extracting navigation, links, content as markdown, and building sitemaps

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  /**
   * Extract all links from the page with context.
   * @param {object} options - Options for extraction
   * @param {boolean} options.internal - Only internal links (default: false)
   * @param {boolean} options.external - Only external links (default: false)
   * @param {boolean} options.includeAnchors - Include anchor-only links (default: false)
   * @param {string} options.selector - Limit to links within selector
   * @returns {object} Links organized by type and context
   */
  function extractLinks(options) {
    options = options || {};
    var baseUrl = window.location.origin;
    var currentPath = window.location.pathname;

    var results = {
      url: window.location.href,
      internal: [],
      external: [],
      anchors: [],
      mailto: [],
      tel: [],
      other: [],
      stats: {
        total: 0,
        internal: 0,
        external: 0,
        anchors: 0,
        broken: 0
      }
    };

    var scope = options.selector ? document.querySelector(options.selector) : document;
    if (!scope) {
      return { error: 'Selector not found: ' + options.selector };
    }

    var links = scope.querySelectorAll('a[href]');
    var seen = new Set();

    for (var i = 0; i < links.length; i++) {
      var link = links[i];
      var href = link.getAttribute('href');
      var fullUrl = link.href;

      // Skip duplicates
      if (seen.has(fullUrl)) continue;
      seen.add(fullUrl);

      var linkData = {
        href: href,
        url: fullUrl,
        text: (link.textContent || '').trim().substring(0, 200),
        title: link.title || null,
        ariaLabel: link.getAttribute('aria-label') || null,
        selector: utils.generateSelector(link),
        inNav: isInNavigation(link),
        inFooter: isInFooter(link),
        inHeader: isInHeader(link),
        rel: link.rel || null
      };

      results.stats.total++;

      // Categorize link
      if (href.startsWith('mailto:')) {
        results.mailto.push(linkData);
      } else if (href.startsWith('tel:')) {
        results.tel.push(linkData);
      } else if (href.startsWith('#')) {
        if (options.includeAnchors) {
          results.anchors.push(linkData);
        }
        results.stats.anchors++;
      } else if (fullUrl.startsWith(baseUrl)) {
        if (!options.external) {
          results.internal.push(linkData);
        }
        results.stats.internal++;
      } else if (fullUrl.startsWith('http')) {
        if (!options.internal) {
          results.external.push(linkData);
        }
        results.stats.external++;
      } else {
        results.other.push(linkData);
      }
    }

    return results;
  }

  /**
   * Extract navigation structure from the page.
   * @returns {object} Navigation elements and their structure
   */
  function extractNavigation() {
    var results = {
      url: window.location.href,
      navElements: [],
      header: null,
      footer: null,
      breadcrumbs: null,
      sidebar: null
    };

    // Find all nav elements
    var navs = document.querySelectorAll('nav, [role="navigation"]');
    for (var i = 0; i < navs.length; i++) {
      var nav = navs[i];
      results.navElements.push(extractNavElement(nav, 'nav-' + i));
    }

    // Find header navigation
    var header = document.querySelector('header, [role="banner"]');
    if (header) {
      var headerLinks = header.querySelectorAll('a[href]');
      if (headerLinks.length > 0) {
        results.header = {
          selector: utils.generateSelector(header),
          links: extractLinksFromElement(header)
        };
      }
    }

    // Find footer navigation
    var footer = document.querySelector('footer, [role="contentinfo"]');
    if (footer) {
      var footerLinks = footer.querySelectorAll('a[href]');
      if (footerLinks.length > 0) {
        results.footer = {
          selector: utils.generateSelector(footer),
          links: extractLinksFromElement(footer)
        };
      }
    }

    // Find breadcrumbs
    var breadcrumb = document.querySelector('[aria-label*="breadcrumb"], .breadcrumb, .breadcrumbs, nav.breadcrumb');
    if (breadcrumb) {
      results.breadcrumbs = {
        selector: utils.generateSelector(breadcrumb),
        items: extractBreadcrumbs(breadcrumb)
      };
    }

    // Find sidebar navigation
    var sidebar = document.querySelector('aside nav, [role="complementary"] nav, .sidebar nav');
    if (sidebar) {
      results.sidebar = extractNavElement(sidebar, 'sidebar');
    }

    return results;
  }

  /**
   * Extract a navigation element's structure.
   */
  function extractNavElement(nav, id) {
    var result = {
      id: id,
      selector: utils.generateSelector(nav),
      ariaLabel: nav.getAttribute('aria-label') || null,
      items: []
    };

    // Look for list-based navigation
    var lists = nav.querySelectorAll(':scope > ul, :scope > ol');
    if (lists.length > 0) {
      result.items = extractNavList(lists[0]);
    } else {
      // Direct links
      result.items = extractLinksFromElement(nav);
    }

    return result;
  }

  /**
   * Extract navigation from a list element (recursive for nested menus).
   */
  function extractNavList(list, depth) {
    depth = depth || 0;
    var items = [];
    var listItems = list.querySelectorAll(':scope > li');

    for (var i = 0; i < listItems.length; i++) {
      var li = listItems[i];
      var link = li.querySelector(':scope > a');
      var subList = li.querySelector(':scope > ul, :scope > ol');

      var item = {
        text: link ? (link.textContent || '').trim() : (li.textContent || '').trim().substring(0, 100),
        href: link ? link.href : null,
        depth: depth,
        current: link ? (link.getAttribute('aria-current') === 'page' || link.classList.contains('active')) : false
      };

      if (subList) {
        item.children = extractNavList(subList, depth + 1);
      }

      items.push(item);
    }

    return items;
  }

  /**
   * Extract links from an element without list structure.
   */
  function extractLinksFromElement(el) {
    var links = el.querySelectorAll('a[href]');
    var items = [];

    for (var i = 0; i < links.length; i++) {
      var link = links[i];
      items.push({
        text: (link.textContent || '').trim(),
        href: link.href,
        current: link.getAttribute('aria-current') === 'page' || link.classList.contains('active')
      });
    }

    return items;
  }

  /**
   * Extract breadcrumb items.
   */
  function extractBreadcrumbs(el) {
    var items = [];
    var links = el.querySelectorAll('a');

    for (var i = 0; i < links.length; i++) {
      items.push({
        text: (links[i].textContent || '').trim(),
        href: links[i].href,
        position: i + 1
      });
    }

    // Check for current page (often not a link)
    var lastText = el.querySelector('span:last-child, li:last-child');
    if (lastText && !lastText.querySelector('a')) {
      items.push({
        text: (lastText.textContent || '').trim(),
        href: null,
        position: items.length + 1,
        current: true
      });
    }

    return items;
  }

  /**
   * Extract page content as markdown.
   * @param {object} options - Options for extraction
   * @param {string} options.selector - Selector for main content (auto-detected if not provided)
   * @param {boolean} options.includeImages - Include image references (default: true)
   * @param {boolean} options.includeLinks - Include link URLs (default: true)
   * @param {number} options.maxLength - Maximum content length (default: 50000)
   * @returns {object} Extracted content as markdown
   */
  function extractContent(options) {
    options = options || {};
    var maxLength = options.maxLength || 50000;
    var includeImages = options.includeImages !== false;
    var includeLinks = options.includeLinks !== false;

    // Find main content area
    var content = null;
    if (options.selector) {
      content = document.querySelector(options.selector);
    } else {
      // Auto-detect main content
      content = document.querySelector('main, [role="main"], article, .content, .post-content, .entry-content, #content');
    }

    if (!content) {
      content = document.body;
    }

    var result = {
      url: window.location.href,
      title: document.title,
      selector: utils.generateSelector(content),
      markdown: '',
      meta: {
        description: getMetaContent('description'),
        keywords: getMetaContent('keywords'),
        author: getMetaContent('author'),
        ogTitle: getMetaContent('og:title'),
        ogDescription: getMetaContent('og:description')
      },
      headings: extractHeadings(content),
      wordCount: 0,
      truncated: false
    };

    // Convert to markdown
    result.markdown = elementToMarkdown(content, { includeImages: includeImages, includeLinks: includeLinks });

    // Truncate if needed
    if (result.markdown.length > maxLength) {
      result.markdown = result.markdown.substring(0, maxLength) + '\n\n[Content truncated...]';
      result.truncated = true;
    }

    // Count words
    result.wordCount = result.markdown.split(/\s+/).filter(function(w) { return w.length > 0; }).length;

    return result;
  }

  /**
   * Extract heading hierarchy.
   */
  function extractHeadings(scope) {
    scope = scope || document;
    var headings = scope.querySelectorAll('h1, h2, h3, h4, h5, h6');
    var result = [];

    for (var i = 0; i < headings.length; i++) {
      var h = headings[i];
      var level = parseInt(h.tagName.substring(1), 10);
      result.push({
        level: level,
        text: (h.textContent || '').trim(),
        id: h.id || null
      });
    }

    return result;
  }

  /**
   * Convert an element to markdown.
   */
  function elementToMarkdown(el, options) {
    var md = '';

    function processNode(node, listDepth) {
      listDepth = listDepth || 0;

      if (node.nodeType === Node.TEXT_NODE) {
        var text = node.textContent;
        // Normalize whitespace but preserve some structure
        text = text.replace(/\s+/g, ' ');
        return text;
      }

      if (node.nodeType !== Node.ELEMENT_NODE) {
        return '';
      }

      var tag = node.tagName.toLowerCase();
      var result = '';

      // Skip hidden elements and scripts
      if (tag === 'script' || tag === 'style' || tag === 'noscript' ||
          node.hidden || node.style.display === 'none') {
        return '';
      }

      // Skip navigation, header, footer for main content
      if (tag === 'nav' || tag === 'header' || tag === 'footer' ||
          node.getAttribute('role') === 'navigation') {
        return '';
      }

      switch (tag) {
        case 'h1':
          result = '\n# ' + getTextContent(node) + '\n\n';
          break;
        case 'h2':
          result = '\n## ' + getTextContent(node) + '\n\n';
          break;
        case 'h3':
          result = '\n### ' + getTextContent(node) + '\n\n';
          break;
        case 'h4':
          result = '\n#### ' + getTextContent(node) + '\n\n';
          break;
        case 'h5':
          result = '\n##### ' + getTextContent(node) + '\n\n';
          break;
        case 'h6':
          result = '\n###### ' + getTextContent(node) + '\n\n';
          break;
        case 'p':
          result = '\n' + processChildren(node, listDepth) + '\n\n';
          break;
        case 'br':
          result = '\n';
          break;
        case 'hr':
          result = '\n---\n\n';
          break;
        case 'strong':
        case 'b':
          result = '**' + processChildren(node, listDepth) + '**';
          break;
        case 'em':
        case 'i':
          result = '*' + processChildren(node, listDepth) + '*';
          break;
        case 'code':
          result = '`' + getTextContent(node) + '`';
          break;
        case 'pre':
          var code = node.querySelector('code');
          var lang = code ? (code.className.match(/language-(\w+)/) || [])[1] || '' : '';
          result = '\n```' + lang + '\n' + getTextContent(node) + '\n```\n\n';
          break;
        case 'blockquote':
          var lines = processChildren(node, listDepth).trim().split('\n');
          result = '\n' + lines.map(function(l) { return '> ' + l; }).join('\n') + '\n\n';
          break;
        case 'ul':
          result = '\n' + processListItems(node, listDepth, '-') + '\n';
          break;
        case 'ol':
          result = '\n' + processListItems(node, listDepth, '1.') + '\n';
          break;
        case 'li':
          var indent = '  '.repeat(listDepth);
          result = indent + processChildren(node, listDepth) + '\n';
          break;
        case 'a':
          if (options.includeLinks && node.href) {
            var linkText = getTextContent(node) || node.href;
            result = '[' + linkText + '](' + node.href + ')';
          } else {
            result = getTextContent(node);
          }
          break;
        case 'img':
          if (options.includeImages && node.src) {
            var alt = node.alt || 'image';
            result = '![' + alt + '](' + node.src + ')';
          }
          break;
        case 'table':
          result = processTable(node);
          break;
        case 'div':
        case 'section':
        case 'article':
        case 'main':
        case 'span':
        default:
          result = processChildren(node, listDepth);
      }

      return result;
    }

    function processChildren(node, listDepth) {
      var result = '';
      for (var i = 0; i < node.childNodes.length; i++) {
        result += processNode(node.childNodes[i], listDepth);
      }
      return result;
    }

    function processListItems(list, depth, marker) {
      var result = '';
      var items = list.querySelectorAll(':scope > li');
      for (var i = 0; i < items.length; i++) {
        var indent = '  '.repeat(depth);
        var itemMarker = marker === '1.' ? (i + 1) + '.' : marker;
        result += indent + itemMarker + ' ' + processChildren(items[i], depth + 1).trim() + '\n';
      }
      return result;
    }

    function processTable(table) {
      var result = '\n';
      var rows = table.querySelectorAll('tr');

      for (var i = 0; i < rows.length; i++) {
        var cells = rows[i].querySelectorAll('th, td');
        var rowContent = [];

        for (var j = 0; j < cells.length; j++) {
          rowContent.push(getTextContent(cells[j]).replace(/\|/g, '\\|'));
        }

        result += '| ' + rowContent.join(' | ') + ' |\n';

        // Add separator after header row
        if (i === 0 && rows[i].querySelector('th')) {
          result += '| ' + rowContent.map(function() { return '---'; }).join(' | ') + ' |\n';
        }
      }

      return result + '\n';
    }

    function getTextContent(node) {
      return (node.textContent || '').trim().replace(/\s+/g, ' ');
    }

    md = processNode(el, 0);

    // Clean up extra whitespace
    md = md.replace(/\n{3,}/g, '\n\n').trim();

    return md;
  }

  /**
   * Build a sitemap structure from internal links.
   * @param {object} options - Options
   * @param {number} options.maxDepth - Maximum URL depth to include (default: 5)
   * @returns {object} Sitemap structure
   */
  function buildSitemap(options) {
    options = options || {};
    var maxDepth = options.maxDepth || 5;
    var baseUrl = window.location.origin;

    var links = extractLinks({ internal: true });
    var sitemap = {
      url: window.location.href,
      baseUrl: baseUrl,
      pages: {},
      tree: {},
      stats: {
        totalPages: 0,
        maxDepth: 0
      }
    };

    // Process internal links
    var allLinks = links.internal;

    for (var i = 0; i < allLinks.length; i++) {
      var link = allLinks[i];
      var url = link.url.split('#')[0].split('?')[0]; // Remove hash and query

      if (sitemap.pages[url]) {
        sitemap.pages[url].references++;
        continue;
      }

      var path = url.replace(baseUrl, '') || '/';
      var parts = path.split('/').filter(function(p) { return p.length > 0; });
      var depth = parts.length;

      if (depth > maxDepth) continue;

      sitemap.pages[url] = {
        url: url,
        path: path,
        depth: depth,
        text: link.text,
        inNav: link.inNav,
        inFooter: link.inFooter,
        references: 1
      };

      sitemap.stats.totalPages++;
      if (depth > sitemap.stats.maxDepth) {
        sitemap.stats.maxDepth = depth;
      }

      // Build tree structure
      var node = sitemap.tree;
      for (var j = 0; j < parts.length; j++) {
        var part = parts[j];
        if (!node[part]) {
          node[part] = { _pages: [] };
        }
        if (j === parts.length - 1) {
          node[part]._pages.push({
            url: url,
            text: link.text
          });
        }
        node = node[part];
      }
    }

    return sitemap;
  }

  /**
   * Extract structured data (JSON-LD, microdata, etc.)
   */
  function extractStructuredData() {
    var result = {
      url: window.location.href,
      jsonLd: [],
      openGraph: {},
      twitter: {},
      microdata: []
    };

    // JSON-LD
    var scripts = document.querySelectorAll('script[type="application/ld+json"]');
    for (var i = 0; i < scripts.length; i++) {
      try {
        var data = JSON.parse(scripts[i].textContent);
        result.jsonLd.push(data);
      } catch (e) {
        result.jsonLd.push({ error: 'Parse error', raw: scripts[i].textContent.substring(0, 500) });
      }
    }

    // Open Graph
    var ogTags = document.querySelectorAll('meta[property^="og:"]');
    for (var j = 0; j < ogTags.length; j++) {
      var prop = ogTags[j].getAttribute('property').replace('og:', '');
      result.openGraph[prop] = ogTags[j].content;
    }

    // Twitter Cards
    var twitterTags = document.querySelectorAll('meta[name^="twitter:"]');
    for (var k = 0; k < twitterTags.length; k++) {
      var name = twitterTags[k].getAttribute('name').replace('twitter:', '');
      result.twitter[name] = twitterTags[k].content;
    }

    return result;
  }

  // Helper functions
  function isInNavigation(el) {
    return !!el.closest('nav, [role="navigation"]');
  }

  function isInFooter(el) {
    return !!el.closest('footer, [role="contentinfo"]');
  }

  function isInHeader(el) {
    return !!el.closest('header, [role="banner"]');
  }

  function getMetaContent(name) {
    var meta = document.querySelector('meta[name="' + name + '"], meta[property="' + name + '"]');
    return meta ? meta.content : null;
  }

  // Export module
  window.__devtool_content = {
    extractLinks: extractLinks,
    extractNavigation: extractNavigation,
    extractContent: extractContent,
    extractHeadings: extractHeadings,
    buildSitemap: buildSitemap,
    extractStructuredData: extractStructuredData
  };

})();
