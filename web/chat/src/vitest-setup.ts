// jsdom-only setup. @testing-library/jest-dom is removed with React.
// Tests assert via standard DOM (textContent, classList, querySelector).

// jsdom lacks scrollIntoView — stub it (chat.ts calls it on bottomSentinel).
if (
  typeof Element !== 'undefined' &&
  !Element.prototype.scrollIntoView
) {
  Element.prototype.scrollIntoView = function () {}
}

export {}
