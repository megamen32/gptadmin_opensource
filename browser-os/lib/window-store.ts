// ============================================================
// GPTAdmin Browser OS — Window Store (Zustand)
// ============================================================

import { create } from 'zustand';
import { WindowState, AppId } from './types';
import { APP_DEFINITIONS } from './mock-data';

interface WindowStore {
  windows: WindowState[];
  nextZIndex: number;

  openWindow: (appId: AppId, computerId?: string) => void;
  closeWindow: (id: string) => void;
  focusWindow: (id: string) => void;
  minimizeWindow: (id: string) => void;
  unminimizeWindow: (id: string) => void;
  maximizeWindow: (id: string) => void;
  updateWindowPosition: (id: string, x: number, y: number) => void;
  updateWindowSize: (id: string, width: number, height: number) => void;
  isWindowOpen: (appId: AppId) => boolean;
  getWindowsByApp: (appId: AppId) => WindowState[];
}

let windowCounter = 0;

function generateWindowId(appId: AppId): string {
  windowCounter++;
  return `${appId}-${windowCounter}`;
}

export const useWindowStore = create<WindowStore>((set, get) => ({
  windows: [],
  nextZIndex: 100,

  openWindow: (appId: AppId) => {
    const existing = get().windows.find(w => w.appId === appId && !w.isMinimized);
    if (existing) {
      get().focusWindow(existing.id);
      get().unminimizeWindow(existing.id);
      return;
    }

    const minimized = get().windows.find(w => w.appId === appId && w.isMinimized);
    if (minimized) {
      get().unminimizeWindow(minimized.id);
      get().focusWindow(minimized.id);
      return;
    }

    const def = APP_DEFINITIONS.find(a => a.id === appId);
    if (!def) return;

    const id = generateWindowId(appId);
    const offset = (get().windows.length % 6) * 30;
    const { nextZIndex } = get();

    const newWindow: WindowState = {
      id,
      appId,
      title: def.name,
      x: 120 + offset,
      y: 60 + offset,
      width: def.defaultWidth,
      height: def.defaultHeight,
      minWidth: def.minWidth,
      minHeight: def.minHeight,
      zIndex: nextZIndex,
      isFocused: true,
      isMinimized: false,
      isMaximized: false,
    };

    set(state => ({
      windows: [
        ...state.windows.map(w => ({ ...w, isFocused: false })),
        newWindow,
      ],
      nextZIndex: nextZIndex + 1,
    }));
  },

  closeWindow: (id: string) => {
    set(state => {
      const remaining = state.windows.filter(w => w.id !== id);
      const hadFocus = state.windows.find(w => w.id === id)?.isFocused;
      if (hadFocus && remaining.length > 0) {
        const topWindow = remaining.reduce((a, b) => (a.zIndex > b.zIndex ? a : b));
        return {
          windows: remaining.map(w =>
            w.id === topWindow.id ? { ...w, isFocused: true } : w
          ),
        };
      }
      return { windows: remaining };
    });
  },

  focusWindow: (id: string) => {
    set(state => {
      const { nextZIndex } = state;
      return {
        windows: state.windows.map(w =>
          w.id === id
            ? { ...w, isFocused: true, zIndex: nextZIndex }
            : { ...w, isFocused: false }
        ),
        nextZIndex: nextZIndex + 1,
      };
    });
  },

  minimizeWindow: (id: string) => {
    set(state => {
      const remaining = state.windows.filter(w => w.id !== id);
      const minimized = state.windows.find(w => w.id === id);
      if (!minimized) return state;

      const newWindows = [
        ...remaining.map(w => ({ ...w, isFocused: false })),
        { ...minimized, isMinimized: true, isFocused: false },
      ];

      if (minimized.isFocused && remaining.length > 0) {
        const topWindow = remaining.reduce((a, b) => (a.zIndex > b.zIndex ? a : b));
        return {
          windows: newWindows.map(w =>
            w.id === topWindow.id ? { ...w, isFocused: true } : w
          ),
        };
      }

      return { windows: newWindows };
    });
  },

  unminimizeWindow: (id: string) => {
    set(state => ({
      windows: state.windows.map(w =>
        w.id === id
          ? { ...w, isMinimized: false, isFocused: true, zIndex: state.nextZIndex }
          : { ...w, isFocused: false }
      ),
      nextZIndex: state.nextZIndex + 1,
    }));
  },

  maximizeWindow: (id: string) => {
    set(state => ({
      windows: state.windows.map(w => {
        if (w.id !== id) return w;
        if (w.isMaximized) {
          return {
            ...w,
            isMaximized: false,
            x: w.prevBounds?.x ?? 120,
            y: w.prevBounds?.y ?? 60,
            width: w.prevBounds?.width ?? 800,
            height: w.prevBounds?.height ?? 500,
            prevBounds: undefined,
          };
        }
        return {
          ...w,
          isMaximized: true,
          prevBounds: { x: w.x, y: w.y, width: w.width, height: w.height },
          x: 0,
          y: 28,
          width: typeof window !== 'undefined' ? window.innerWidth : 1440,
          height: typeof window !== 'undefined' ? window.innerHeight - 28 - 72 : 800,
        };
      }),
    }));
  },

  updateWindowPosition: (id: string, x: number, y: number) => {
    set(state => ({
      windows: state.windows.map(w =>
        w.id === id ? { ...w, x, y, isMaximized: false, prevBounds: undefined } : w
      ),
    }));
  },

  updateWindowSize: (id: string, width: number, height: number) => {
    set(state => ({
      windows: state.windows.map(w =>
        w.id === id
          ? { ...w, width: Math.max(w.minWidth, width), height: Math.max(w.minHeight, height), isMaximized: false, prevBounds: undefined }
          : w
      ),
    }));
  },

  isWindowOpen: (appId: AppId) => {
    return get().windows.some(w => w.appId === appId);
  },

  getWindowsByApp: (appId: AppId) => {
    return get().windows.filter(w => w.appId === appId);
  },
}));
