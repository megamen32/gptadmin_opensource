// ============================================================
// GPTAdmin Browser OS — Window Component
// ============================================================

'use client';

import React, { useCallback, useRef, useEffect, useState } from 'react';
import { WindowState } from '@/lib/types';
import { useWindowStore } from '@/lib/window-store';

interface WindowProps {
  windowState: WindowState;
  children: React.ReactNode;
}

const RESIZE_HANDLE_SIZE = 8;
const MIN_DRAG_THRESHOLD = 2;

export function Window({ windowState, children }: WindowProps) {
  const {
    closeWindow,
    focusWindow,
    minimizeWindow,
    maximizeWindow,
    updateWindowPosition,
    updateWindowSize,
  } = useWindowStore();

  const windowRef = useRef<HTMLDivElement>(null);
  const dragRef = useRef<{ startX: number; startY: number; winX: number; winY: number; dragging: boolean }>({
    startX: 0, startY: 0, winX: 0, winY: 0, dragging: false,
  });
  const resizeRef = useRef<{
    startX: number;
    startY: number;
    winX: number;
    winY: number;
    winW: number;
    winH: number;
    direction: string;
    resizing: boolean;
  }>({ startX: 0, startY: 0, winX: 0, winY: 0, winW: 0, winH: 0, direction: '', resizing: false });

  const [isHoveringClose, setIsHoveringClose] = useState(false);
  const [isHoveringMinimize, setIsHoveringMinimize] = useState(false);
  const [isHoveringMaximize, setIsHoveringMaximize] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [isResizing, setIsResizing] = useState(false);

  const handleMouseDownTitleBar = useCallback((e: React.MouseEvent) => {
    if ((e.target as HTMLElement).closest('[data-traffic-light]')) return;
    e.preventDefault();
    focusWindow(windowState.id);
    dragRef.current = {
      startX: e.clientX,
      startY: e.clientY,
      winX: windowState.x,
      winY: windowState.y,
      dragging: true,
    };
  }, [windowState.id, windowState.x, windowState.y, focusWindow]);

  const handleMouseDownResize = useCallback((e: React.MouseEvent, direction: string) => {
    e.preventDefault();
    e.stopPropagation();
    focusWindow(windowState.id);
    resizeRef.current = {
      startX: e.clientX,
      startY: e.clientY,
      winX: windowState.x,
      winY: windowState.y,
      winW: windowState.width,
      winH: windowState.height,
      direction,
      resizing: true,
    };
  }, [windowState.id, windowState.x, windowState.y, windowState.width, windowState.height, focusWindow]);

  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      const drag = dragRef.current;
      if (drag.dragging) {
        const dx = e.clientX - drag.startX;
        const dy = e.clientY - drag.startY;
        if (!isDragging && (Math.abs(dx) > MIN_DRAG_THRESHOLD || Math.abs(dy) > MIN_DRAG_THRESHOLD)) {
          setIsDragging(true);
        }
        if (isDragging) {
          updateWindowPosition(windowState.id, drag.winX + dx, Math.max(28, drag.winY + dy));
        }
      }

      const resize = resizeRef.current;
      if (resize.resizing) {
        setIsResizing(true);
        const dx = e.clientX - resize.startX;
        const dy = e.clientY - resize.startY;
        let newX = resize.winX;
        let newY = resize.winY;
        let newW = resize.winW;
        let newH = resize.winH;

        if (resize.direction.includes('e')) newW = resize.winW + dx;
        if (resize.direction.includes('w')) {
          newW = resize.winW - dx;
          newX = resize.winX + dx;
        }
        if (resize.direction.includes('s')) newH = resize.winH + dy;
        if (resize.direction.includes('n')) {
          newH = resize.winH - dy;
          newY = Math.max(28, resize.winY + dy);
        }

        updateWindowSize(windowState.id, newW, newH);
        if (resize.direction.includes('w') || resize.direction.includes('n')) {
          updateWindowPosition(windowState.id, newX, newY);
        }
      }
    };

    const handleMouseUp = () => {
      if (dragRef.current.dragging) {
        dragRef.current.dragging = false;
        setIsDragging(false);
      }
      if (resizeRef.current.resizing) {
        resizeRef.current.resizing = false;
        setIsResizing(false);
      }
    };

    const handleDoubleClickTitleBar = (e: MouseEvent) => {
      if ((e.target as HTMLElement).closest('[data-traffic-light]')) return;
      const titleBar = windowRef.current?.querySelector('[data-title-bar]');
      if (titleBar?.contains(e.target as Node)) {
        maximizeWindow(windowState.id);
      }
    };

    window.addEventListener('mousemove', handleMouseMove);
    window.addEventListener('mouseup', handleMouseUp);
    window.addEventListener('dblclick', handleDoubleClickTitleBar);

    return () => {
      window.removeEventListener('mousemove', handleMouseMove);
      window.removeEventListener('mouseup', handleMouseUp);
      window.removeEventListener('dblclick', handleDoubleClickTitleBar);
    };
  }, [windowState.id, isDragging, updateWindowPosition, updateWindowSize, maximizeWindow]);

  if (windowState.isMinimized) return null;

  const handleResizeEdge = (direction: string) => {
    const cursors: Record<string, string> = {
      n: 'cursor-n-resize', s: 'cursor-s-resize',
      e: 'cursor-e-resize', w: 'cursor-w-resize',
      ne: 'cursor-ne-resize', nw: 'cursor-nw-resize',
      se: 'cursor-se-resize', sw: 'cursor-sw-resize',
    };
    return cursors[direction] || '';
  };

  return (
    <div
      ref={windowRef}
      className={`absolute flex flex-col overflow-hidden select-none transition-shadow duration-150 ${
        windowState.isFocused
          ? 'shadow-[0_8px_32px_rgba(0,0,0,0.45),0_0_0_1px_rgba(255,255,255,0.08)]'
          : 'shadow-[0_4px_16px_rgba(0,0,0,0.3),0_0_0_1px_rgba(255,255,255,0.04)]'
      } ${windowState.isMaximized ? 'rounded-none' : 'rounded-xl'} ${isDragging || isResizing ? '' : 'transition-[border-radius] duration-200'}`}
      style={{
        left: windowState.x,
        top: windowState.y,
        width: windowState.width,
        height: windowState.height,
        zIndex: windowState.zIndex,
      }}
      onMouseDown={() => focusWindow(windowState.id)}
    >
      {/* Title Bar */}
      <div
        data-title-bar
        className={`flex items-center h-12 px-4 shrink-0 ${
          windowState.isFocused
            ? 'bg-[#2a2a3e]/95'
            : 'bg-[#2a2a3e]/80'
        } backdrop-blur-xl border-b border-white/5`}
        onMouseDown={handleMouseDownTitleBar}
        onDoubleClick={() => maximizeWindow(windowState.id)}
      >
        {/* Traffic Lights */}
        <div data-traffic-light className="flex items-center gap-2 mr-4">
          <button
            className={`w-3 h-3 rounded-full flex items-center justify-center transition-all duration-100 ${
              isHoveringClose ? 'bg-[#ff5f57]' : 'bg-[#ff5f57]/80 hover:bg-[#ff5f57]'
            }`}
            onMouseEnter={() => setIsHoveringClose(true)}
            onMouseLeave={() => setIsHoveringClose(false)}
            onClick={(e) => { e.stopPropagation(); closeWindow(windowState.id); }}
            aria-label="Close"
          >
            {isHoveringClose && (
              <svg width="6" height="6" viewBox="0 0 6 6"><path d="M0 0 L6 6 M6 0 L0 6" stroke="#4a0002" strokeWidth="1.2" /></svg>
            )}
          </button>
          <button
            className={`w-3 h-3 rounded-full flex items-center justify-center transition-all duration-100 ${
              isHoveringMinimize ? 'bg-[#febc2e]' : 'bg-[#febc2e]/80 hover:bg-[#febc2e]'
            }`}
            onMouseEnter={() => setIsHoveringMinimize(true)}
            onMouseLeave={() => setIsHoveringMinimize(false)}
            onClick={(e) => { e.stopPropagation(); minimizeWindow(windowState.id); }}
            aria-label="Minimize"
          >
            {isHoveringMinimize && (
              <svg width="6" height="2" viewBox="0 0 6 2"><rect width="6" height="2" rx="1" fill="#995700" /></svg>
            )}
          </button>
          <button
            className={`w-3 h-3 rounded-full flex items-center justify-center transition-all duration-100 ${
              isHoveringMaximize ? 'bg-[#28c840]' : 'bg-[#28c840]/80 hover:bg-[#28c840]'
            }`}
            onMouseEnter={() => setIsHoveringMaximize(true)}
            onMouseLeave={() => setIsHoveringMaximize(false)}
            onClick={(e) => { e.stopPropagation(); maximizeWindow(windowState.id); }}
            aria-label="Maximize"
          >
            {isHoveringMaximize && (
              <svg width="6" height="6" viewBox="0 0 6 6">
                <rect x="0.5" y="0.5" width="5" height="5" rx="1" fill="none" stroke="#006500" strokeWidth="1" />
              </svg>
            )}
          </button>
        </div>

        {/* Title */}
        <div className={`flex-1 text-center text-sm font-medium truncate ${
          windowState.isFocused ? 'text-white/90' : 'text-white/50'
        }`}>
          {windowState.title}
        </div>

        <div className="w-[60px]" />
      </div>

      {/* Content */}
      <div className="flex-1 overflow-hidden bg-[#1e1e2e]">
        {children}
      </div>

      {/* Resize Handles */}
      {!windowState.isMaximized && (
        <>
          {/* Edges */}
          <div className={`absolute top-0 left-0 right-0 h-[${RESIZE_HANDLE_SIZE}px] ${handleResizeEdge('n')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'n')} />
          <div className={`absolute bottom-0 left-0 right-0 h-[${RESIZE_HANDLE_SIZE}px] ${handleResizeEdge('s')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 's')} />
          <div className={`absolute top-0 left-0 bottom-0 w-[${RESIZE_HANDLE_SIZE}px] ${handleResizeEdge('w')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'w')} />
          <div className={`absolute top-0 right-0 bottom-0 w-[${RESIZE_HANDLE_SIZE}px] ${handleResizeEdge('e')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'e')} />

          {/* Corners */}
          <div className={`absolute top-0 left-0 w-3 h-3 ${handleResizeEdge('nw')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'nw')} />
          <div className={`absolute top-0 right-0 w-3 h-3 ${handleResizeEdge('ne')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'ne')} />
          <div className={`absolute bottom-0 left-0 w-3 h-3 ${handleResizeEdge('sw')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'sw')} />
          <div className={`absolute bottom-0 right-0 w-3 h-3 ${handleResizeEdge('se')}`}
               onMouseDown={(e) => handleMouseDownResize(e, 'se')} />
        </>
      )}
    </div>
  );
}