package main

// layoutCheck is the house's layout audit, handed to every browser
// professional to run with browser_evaluate at each width it tests. People
// notice layout faults by eye, inconsistently; this lists the candidates
// (text cut off by its box, text squeezed to a sliver, content clipped by a
// fixed height, anything past the right edge, actions below the fold) for
// the professional to confirm on screen. It reads the page; it changes
// nothing.
const layoutCheck = `() => {
  const vw = innerWidth, vh = innerHeight, out = [];
  const label = el => el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') +
    (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '');
  for (const el of document.querySelectorAll('body *')) {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || +cs.opacity === 0) continue;
    const r = el.getBoundingClientRect();
    if (!r.width && !r.height) continue;
    const own = [...el.childNodes].some(n => n.nodeType === 3 && n.textContent.trim());
    const text = (el.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 50);
    if (own && el.scrollWidth > el.clientWidth + 1 && (cs.overflowX !== 'visible' || cs.textOverflow === 'ellipsis'))
      out.push({issue: 'text cut off', el: label(el), text, needs: el.scrollWidth, has: el.clientWidth});
    if (own && r.width > 0 && r.width < 24 && text.length > 2)
      out.push({issue: 'squeezed', el: label(el), text, width: Math.round(r.width)});
    if (own && el.scrollHeight > el.clientHeight + 1 && (cs.overflowY === 'hidden' || cs.overflowY === 'clip'))
      out.push({issue: 'clipped', el: label(el), text, needs: el.scrollHeight, has: el.clientHeight});
    if (r.right > vw + 1 && cs.position !== 'fixed' && r.left < vw)
      out.push({issue: 'past the right edge', el: label(el), text, right: Math.round(r.right)});
  }
  const below = [...document.querySelectorAll('button, a[href], [role=button], input[type=submit]')]
    .filter(b => { const r = b.getBoundingClientRect(); return r.width && r.top >= vh; })
    .map(b => ((b.innerText || b.value || b.getAttribute('aria-label') || '').trim().slice(0, 40) + ' @y' + Math.round(b.getBoundingClientRect().top)))
    .filter(s => !s.startsWith(' @'));
  return {viewport: vw + 'x' + vh, scroll_width: document.documentElement.scrollWidth, issues: out.slice(0, 40), actions_below_the_fold: below.slice(0, 20)};
}`

// lookCheck is what a person sees, for browser professionals to run after
// every action that should show a result. The snapshot lists every element
// on the page, the ones below the window included, and calls a control a
// button when the code does; a person sees only the window, and sees a
// button only when it's drawn like one. So it reports the text that appeared
// since the last look (kept on the page between calls) and where it is
// against the window, and how each control in the window is drawn. It reads
// the page; it changes nothing.
const lookCheck = `() => {
  const vh = innerHeight, vw = innerWidth;
  const seen = window.__administrationLook || new Set(), now = new Set(), appeared = [];
  const where = r => r.bottom <= 0 ? 'above the window by ' + Math.round(-r.bottom) + 'px'
    : r.top >= vh ? 'below the window by ' + Math.round(r.top - vh) + 'px'
    : r.bottom > vh ? 'starts in the window, cut off at the bottom' : 'in the window';
  const shown = el => { const cs = getComputedStyle(el), r = el.getBoundingClientRect();
    return cs.display !== 'none' && cs.visibility !== 'hidden' && +cs.opacity > 0 && r.width > 0 && r.height > 0; };
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n; (n = walker.nextNode());) {
    const t = n.textContent.trim().replace(/\s+/g, ' ');
    if (t.length < 2 || !n.parentElement || !shown(n.parentElement)) continue;
    now.add(t);
    if (window.__administrationLook && !seen.has(t) && appeared.length < 15)
      appeared.push({text: t.slice(0, 80), where: where(n.parentElement.getBoundingClientRect())});
  }
  window.__administrationLook = now;
  const opaque = c => { const m = c.match(/[\d.]+/g); return m && (m.length < 4 || +m[3] > 0.05); };
  const bgOf = el => { for (let e = el; e; e = e.parentElement) { const c = getComputedStyle(e).backgroundColor; if (opaque(c)) return c; } return 'rgb(255, 255, 255)'; };
  const looks = el => {
    const cs = getComputedStyle(el);
    const border = ['Top', 'Right', 'Bottom', 'Left'].filter(s => parseFloat(cs['border' + s + 'Width']) > 0 &&
      cs['border' + s + 'Style'] !== 'none' && opaque(cs['border' + s + 'Color'])).length >= 3;
    const fill = opaque(cs.backgroundColor) && el.parentElement && cs.backgroundColor !== bgOf(el.parentElement);
    if (border || fill || (cs.boxShadow && cs.boxShadow !== 'none')) return 'drawn as a button';
    if (cs.textDecorationLine.includes('underline')) return 'underlined, like a link';
    if (el.matches('input:not([type=submit]):not([type=button]), select, textarea')) return 'a field';
    return 'plain text (no border, fill or underline)';
  };
  const controls = [];
  for (const el of document.querySelectorAll('button, a[href], [role=button], summary, input[type=submit], input[type=button], [onclick]')) {
    if (!shown(el)) continue;
    const r = el.getBoundingClientRect();
    if (r.bottom <= 0 || r.top >= vh || r.right <= 0 || r.left >= vw) continue;
    const label = (el.innerText || el.value || el.getAttribute('aria-label') || '').trim().replace(/\s+/g, ' ').slice(0, 40);
    if (label && controls.length < 40) controls.push({control: label, looks: looks(el), size: Math.round(r.width) + 'x' + Math.round(r.height)});
  }
  return {window: vw + 'x' + vh, scrolled: Math.round(scrollY), page_height: document.documentElement.scrollHeight,
    appeared_since_last_look: window.__administrationLookFirst ? appeared : 'first look: run it again after your next action',
    controls_in_the_window: controls, _: (window.__administrationLookFirst = true, undefined)};
}`
