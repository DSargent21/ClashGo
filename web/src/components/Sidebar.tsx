import React from 'react';
import { TabType } from '../types';
import Icon, { IconName } from './ui/Icon';
import logo from '../assets/images/clashgo-logo.png';

interface SidebarProps {
  tab: TabType;
  setTab: (tab: TabType) => void;
  expanded: boolean;
  running: boolean;
  onStart: () => void;
  onStop: () => void;
}

interface NavItem {
  id: TabType;
  label: string;
  icon: IconName;
}

const OVERVIEW: NavItem[] = [
  { id: 'dashboard', label: 'Dashboard', icon: 'dashboard' },
  { id: 'army', label: 'Army', icon: 'layers' },
  { id: 'analytics', label: 'Analytics', icon: 'chart' },
];

const FLEET: NavItem[] = [
  { id: 'accounts', label: 'Accounts', icon: 'users' },
];

const CONFIGURE: NavItem[] = [
  { id: 'config', label: 'Config', icon: 'sliders' },
  { id: 'settings', label: 'Settings', icon: 'settings' },
];

const Sidebar: React.FC<SidebarProps> = React.memo(({
  tab,
  setTab,
  expanded,
  running,
  onStart,
  onStop,
}) => {
  const renderNav = (items: NavItem[]) => items.map((item) => {
    const active = tab === item.id;
    return (
      <button
        key={item.id}
        type="button"
        onClick={() => setTab(item.id)}
        className={`nav-btn ${active ? 'active' : ''}`.trim()}
        title={expanded ? undefined : item.label}
        aria-label={item.label}
        aria-current={active ? 'page' : undefined}
      >
        <Icon name={item.icon} size={17} />
        <span className="nav-label">{item.label}</span>
      </button>
    );
  });

  return (
    <aside id="app-sidebar" className={`sidebar ${expanded ? '' : 'collapsed'}`.trim()}>
      <div className="side-drag draggable" />

      <div className="brand">
        <div className="brand-mark">
          <img src={logo} alt="" className="no-drag" />
        </div>
        <div className="brand-text">
          <b>ClashGO</b>
          <span>Mission control</span>
        </div>
      </div>

      <nav className="side-nav" aria-label="Primary">
        <div className="nav-group">Overview</div>
        {renderNav(OVERVIEW)}
        <div className="nav-group">Fleet</div>
        {renderNav(FLEET)}
        <div className="nav-group">Configure</div>
        {renderNav(CONFIGURE)}
      </nav>

      <div className="side-foot">
        <button
          type="button"
          onClick={running ? onStop : onStart}
          className={`engine-power ${running ? 'stop' : 'start'}`.trim()}
          aria-label={running ? 'Stop bot' : 'Start bot'}
          title={running ? 'Stop bot' : 'Start bot'}
        >
          <Icon name={running ? 'stop' : 'play'} size={11} />
          <span>{running ? 'Stop bot' : 'Start bot'}</span>
        </button>
      </div>
    </aside>
  );
});

Sidebar.displayName = 'Sidebar';

export default Sidebar;
