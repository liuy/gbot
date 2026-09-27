// gbot fork addition (not upstream).

// Kept in a module of its own so features/controls.ts can read the walk
// guards and features/walk.ts can own them without the two mixins
// importing each other.
export const $walkActive = Symbol('walkActive');
export const $walkController = Symbol('walkController');
