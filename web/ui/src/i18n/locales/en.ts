// Source dictionary — the type truth every other locale must satisfy. en is
// also the shipped UI copy when locale=en, so values here must match the
// settings-page strings verbatim (several are asserted in settings.test.ts).
// Deliberately NOT translated — protocol identifiers, product names,
// endonyms, glyphs and data fallbacks stay literal at call sites:
//   - the header "JSON" action; protocol tile names and endpoint paths
//     (OpenAI / Responses / Anthropic, /chat/completions …)
//   - TYPE_LABELS badges (AUTO/OPENAI/RESPONSES/ANTHROPIC)
//   - THINK_OPTS values (none/auto/low/… are stored config values)
//   - HLJS theme names (Atom One … are product names)
//   - glyphs and data strings (› ✕ … ✓ …ms ✗ …)
//   - locale endonyms English/简体中文/繁體中文 (each locale file's endonym export)
//   - the sidebar "Artifacts" heading and the builtin game name
//   - sidebar formatRelativeTime's compact units (now/30s/5m/1h/3mo —
//     data strings, like the …ms glyph above)
//   - interpolation data fallbacks 'provider' and 'error' passed from
//     settings.ts call sites (not UI copy)

export const dict = {
  settingsTitle: 'Settings',
  addProviderTitle: 'Add provider',
  editProviderTitle: 'Edit provider',
  addProviderBtn: 'Add Provider',
  providersSection: 'PROVIDERS',
  defaultModelSection: 'DEFAULT MODEL',
  generalSection: 'GENERAL',
  languageRow: 'Language',
  themeRow: 'Theme',
  highlightsRow: 'Highlights',
  systemTheme: 'System',
  languageSystem: 'System',
  lightTheme: 'Light',
  darkTheme: 'Dark',
  savedRestart: 'Saved · restart daemon to apply',
  savedBackedUp: 'Saved · original backed up · restart daemon to apply',
  deletedRestorable: 'Deleted · restorable from backup',
  saveFailed: (m: string) => `Save failed: ${m}`,
  loadFailed: (m: string) => `Load failed: ${m}`,
  nameUrlRequired: 'Name and URL are required',
  oneModelRequired: 'At least one named model is required',
  oneProviderRequired: 'At least one provider is required',
  deleteProviderRow: 'Delete this provider',
  deleteProviderConfirm:
    'Delete this provider?\nThe original settings.json was backed up and can be restored.',
  deleteDefaultConfirm:
    '⚠ This provider owns the current default model — deleting it breaks the default.\n\nDelete anyway?',
  protocolSection: 'PROTOCOL',
  nameLabel: 'NAME',
  baseUrlLabel: 'BASE URL',
  apiKeySection: 'API KEYS',
  triedInOrder: ' · tried in order',
  modelsSection: 'MODELS',
  extraParamsLabel: 'EXTRA PARAMS · appended to request body',
  testBtn: 'Test',
  pinging: (m: string) => `Pinging ${m}…`,
  addKey: 'Add Key',
  addModel: 'Add Model',
  addParam: 'Add Param',
  fetchFromApi: '· fetch from API',
  fetchFreeTop10: '· fetch free top 10',
  fetching: '· fetching…',
  cancelBtn: 'Cancel',
  saveBtn: 'Save',
  contextLabel: 'CONTEXT',
  maxTokensLabel: 'MAX TOKENS',
  inputLabel: 'INPUT',
  thinkingLabel: 'THINKING',
  textModality: 'Text',
  imageModality: 'Image',
  audioModality: 'Audio',
  videoModality: 'Video',
  modelsCount: (n: number) => `${n} models`,
  fetchedModels: (total: number, added: number) =>
    `Fetched ${total} models, ${added} added (existing kept)`,
  allModelsConfigured: 'All models already configured',
  anthropicNoModelList: 'Anthropic has no model list endpoint — add models manually',
  phProviderName: 'provider name',
  phApiKey: 'API key',
  phModelId: 'model id',
  phParamName: 'param name, e.g. temperature',
  phParamValue: 'value (JSON literal)',
  showHideTitle: 'show/hide',
  jsonSheetTitle: 'settings.json · providers',
  sidebarSessions: 'Sessions',
  sidebarNewSession: 'New session',
  sidebarGames: 'Games',
  sidebarGameChess: 'Chinese Chess',
  sidebarClearArtifacts: 'Clear all artifacts',
  sidebarSettings: 'Settings',
  sidebarNoArtifacts: 'No artifacts yet',
} as const

export const endonym = 'English'
