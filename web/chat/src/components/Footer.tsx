type Tab = 'chat' | 'settings'

export default function Footer({
  tab,
  onChange,
}: {
  tab: Tab
  onChange: (t: Tab) => void
}) {
  return (
    <footer className="sticky bottom-0 z-20 glass border-t border-hairline">
      <TabButton
        label="Chat"
        active={tab === 'chat'}
        onClick={() => onChange('chat')}
      />
      <TabButton
        label="Settings"
        active={tab === 'settings'}
        onClick={() => onChange('settings')}
      />
    </footer>
  )
}

function TabButton({
  label,
  active,
  onClick,
}: {
  label: string
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        'relative flex-1 py-3 text-center text-sm ' +
        (active ? 'text-primary' : 'text-t3')
      }
    >
      {label}
      {active && (
        <span className="absolute inset-x-4 top-0 h-px bg-primary" />
      )}
    </button>
  )
}
