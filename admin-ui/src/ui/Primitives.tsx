import type { ReactNode } from "react";

type PageHeaderProps = {
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
  className?: string;
};

export function PageHeader({ eyebrow, title, description, actions, className = "" }: PageHeaderProps) {
  return <header className={`page-header compact-page-header ${className}`.trim()}>
    <div>
      {eyebrow && <p className="section-kicker">{eyebrow}</p>}
      <h1>{title}</h1>
      {description && <p className="lede">{description}</p>}
    </div>
    {actions && <div className="page-header-actions">{actions}</div>}
  </header>;
}

export function ErrorNotice({ message }: { message: string | null }) {
  return message ? <div className="state-panel card standalone-state state-error compact-notice" role="alert">{message}</div> : null;
}

export function EmptyState({ title, description }: { title: string; description?: string }) {
  return <div className="compact-empty"><strong>{title}</strong>{description && <span>{description}</span>}</div>;
}

export function TechnicalDetails({ title, children, className = "" }: { title: string; children: ReactNode; className?: string }) {
  return <details className={`technical-details ${className}`.trim()}><summary>{title}</summary>{children}</details>;
}

export function StatusBadge({ children, state = "ready" }: { children: ReactNode; state?: "ready" | "error" | "neutral" }) {
  return <span className={`data-badge state-${state}`} role="status">{children}</span>;
}
