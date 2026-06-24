export function Logo() {
  return (
    <span className="brand">
      <svg width="22" height="22" viewBox="0 0 30 30" aria-hidden="true">
        <rect x="3" y="3" width="24" height="24" rx="6" fill="none" stroke="var(--teal)" strokeWidth="2" />
        <path d="M9 11 l4 4 -4 4" fill="none" stroke="var(--olive)" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
        <rect x="16" y="17" width="6" height="2.4" rx="1" fill="var(--olive)" />
      </svg>
      <span className="word">marshal<span className="cur">_</span></span>
    </span>
  );
}
