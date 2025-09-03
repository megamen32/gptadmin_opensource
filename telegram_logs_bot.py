#!/usr/bin/env python3
import asyncio
import json
import re
import subprocess
from pathlib import Path
from datetime import datetime, timezone
from aiogram import Bot, Dispatcher, types
from aiogram.types import InlineKeyboardMarkup, InlineKeyboardButton
from aiogram.filters import Command

"""
V4 — что добавлено:
- Полноценные настройки Ключевых слов и Игнора из чата:
  • Кнопки «✏️ Ключевые слова» и «🚫 Игнор» переводят бота в режим редактирования.
  • Пришлите список через запятую/с новой строки — список будет ПЕРЕЗАПИСАН.
  • Используйте префиксы "+слово" для добавления и "-слово" для удаления (инкрементально).
  • /cancel — выйти из режима редактирования.
- Переключатель регистронезависимого поиска (Aa): cfg["case_insensitive"].
- Уведомления содержат уровень, идентификатор процесса, PID, unit и локальное время.
- Отбор логов: все сообщения с приоритетом <= threshold отправляются всегда;
  из остальных — только если есть ключевое слово и нет игнора.
"""

# === Настройки ===
TOKEN = "<TELEGRAM_TOKEN>"
CHAT_ID = 540308572
CONFIG_FILE = Path(__file__).parent / "logs_config.json"

# === Конфиг по умолчанию ===
default_config = {
    "level": "err",
    "keywords": ["FAILED", "segfault", "panic", "oom-killer"],
    "ignore": [],
    "enabled": True,
    "case_insensitive": False,
    "awaiting": None,  # None | "keywords" | "ignore"
}


def load_config():
    if CONFIG_FILE.exists():
        try:
            return json.loads(CONFIG_FILE.read_text())
        except Exception:
            pass
    save_config(default_config)
    return default_config


def save_config(cfg):
    CONFIG_FILE.write_text(json.dumps(cfg, indent=2, ensure_ascii=False))


config = load_config()

# === Инициализация бота ===
bot = Bot(token=TOKEN)
dp = Dispatcher()

# === UI ===

def main_menu():
    cfg = load_config()
    case_txt = "Aa: insens" if cfg.get("case_insensitive") else "Aa: sens"
    kb = InlineKeyboardMarkup(
        inline_keyboard=[
            [InlineKeyboardButton(text=f"🔽 Уровень: {cfg['level']}", callback_data="setlevel")],
            [InlineKeyboardButton(text="✏️ Ключевые слова", callback_data="keywords")],
            [InlineKeyboardButton(text="🚫 Игнор", callback_data="ignore")],
            [InlineKeyboardButton(text=case_txt, callback_data="toggle_case")],
            [
                InlineKeyboardButton(text=("▶️ Запустить" if not cfg["enabled"] else "⏸️ Остановить"), callback_data="toggle"),
                InlineKeyboardButton(text="📊 Статус", callback_data="status"),
            ],
        ]
    )
    return kb


# === Команды ===
@dp.message(Command("start"))
async def cmd_start(message: types.Message):
    if message.chat.id != CHAT_ID:
        return
    await message.answer("⚙️ Настройки логов", reply_markup=main_menu())


@dp.message(Command("cancel"))
async def cmd_cancel(message: types.Message):
    if message.chat.id != CHAT_ID:
        return
    cfg = load_config()
    if cfg.get("awaiting"):
        cfg["awaiting"] = None
        save_config(cfg)
        await message.answer("❎ Редактирование отменено", reply_markup=main_menu())


# Дублируем быстрые команды
@dp.message(Command("keywords"))
async def cmd_keywords(message: types.Message):
    if message.chat.id != CHAT_ID:
        return
    await enter_edit_mode(message, "keywords")


@dp.message(Command("ignore"))
async def cmd_ignore(message: types.Message):
    if message.chat.id != CHAT_ID:
        return
    await enter_edit_mode(message, "ignore")


async def enter_edit_mode(message: types.Message, field: str):
    cfg = load_config()
    cfg["awaiting"] = field
    save_config(cfg)
    current = cfg.get(field, [])
    nice = ", ".join(current) if current else "(пусто)"
    title = "Ключевые слова" if field == "keywords" else "Игнор"
    hint = (
        f"✏️ {title}: сейчас → {nice}\n\n"
        "Пришлите новый список через запятую/строки — перезапишу.\n"
        "Или используйте инкрементально: +слово чтобы добавить, -слово чтобы удалить.\n"
        "Примеры:\n"
        "  +FAILED,+panic\n  -segfault\n  oom-killer, panic, coredump\n\n"
        "Команда /cancel — выйти без изменений."
    )
    await message.answer(hint)


# === Callback-и ===
@dp.callback_query()
async def callbacks(call: types.CallbackQuery):
    if call.message.chat.id != CHAT_ID:
        return

    cfg = load_config()

    if call.data == "setlevel":
        levels = ["debug", "info", "notice", "warning", "err", "crit", "alert", "emerg"]
        i = levels.index(cfg.get("level", "err")) if cfg.get("level") in levels else 4
        cfg["level"] = levels[(i + 1) % len(levels)]
        save_config(cfg)
        await call.message.edit_reply_markup(reply_markup=main_menu())

    elif call.data == "toggle":
        cfg["enabled"] = not cfg["enabled"]
        save_config(cfg)
        await call.message.edit_reply_markup(reply_markup=main_menu())

    elif call.data == "toggle_case":
        cfg["case_insensitive"] = not cfg.get("case_insensitive", False)
        save_config(cfg)
        await call.message.edit_reply_markup(reply_markup=main_menu())

    elif call.data == "status":
        text = (
            f"⚙️ Текущие настройки:\n"
            f"Уровень: {cfg['level']}+\n"
            f"Ключевые слова: {', '.join(cfg.get('keywords', [])) or '(нет)'}\n"
            f"Игнор: {', '.join(cfg.get('ignore', [])) or '(нет)'}\n"
            f"Регистр: {'insens' if cfg.get('case_insensitive') else 'sens'}\n"
            f"Состояние: {'▶️ ВКЛ' if cfg.get('enabled') else '⏸️ ВЫКЛ'}"
        )
        await call.message.answer(text)

    elif call.data == "keywords":
        await enter_edit_mode(call.message, "keywords")

    elif call.data == "ignore":
        await enter_edit_mode(call.message, "ignore")

    await call.answer()


# === Редактор списков: общий текстовый хэндлер ===
@dp.message()
async def any_text(message: types.Message):
    if message.chat.id != CHAT_ID or not message.text:
        return

    cfg = load_config()
    awaiting = cfg.get("awaiting")
    if not awaiting:
        return

    text = message.text.strip()
    if text.startswith("/"):
        return  # позволим командам работать отдельно

    field = awaiting  # "keywords" | "ignore"

    # Парсим ввод: поддерживаем "+слово" / "-слово" и полную замену
    tokens_raw = re.split(r"[,\n]", text)
    tokens = [t.strip() for t in tokens_raw if t.strip()]

    adds = [t[1:].strip() for t in tokens if t.startswith("+") and len(t) > 1]
    dels = [t[1:].strip() for t in tokens if t.startswith("-") and len(t) > 1]
    base = [t for t in tokens if t and not (t.startswith("+") or t.startswith("-"))]

    current = list(cfg.get(field, []) or [])
    cur_set = {x for x in current}

    if (adds or dels) and not base:
        # Инкрементально
        for w in adds:
            if w:
                cur_set.add(w)
        for w in dels:
            if w and w in cur_set:
                cur_set.remove(w)
        new_list = sorted(cur_set)
    else:
        # Полная замена
        new_list = sorted({x for x in base})

    cfg[field] = new_list
    cfg["awaiting"] = None
    save_config(cfg)

    title = "Ключевые слова" if field == "keywords" else "Игнор"
    nice = ", ".join(new_list) if new_list else "(пусто)"
    await message.answer(f"✅ {title} обновлены: {nice}", reply_markup=main_menu())


# === Фоновая задача для логов ===
async def log_watcher():
    # PRIORITY 0..7  (0=emerg, 7=debug)
    name_to_num = {
        "emerg": 0,
        "alert": 1,
        "crit": 2,
        "err": 3,
        "warning": 4,
        "notice": 5,
        "info": 6,
        "debug": 7,
    }
    num_to_meta = {
        0: ("🆘", "emerg"),
        1: ("🚨", "alert"),
        2: ("🔴", "crit"),
        3: ("🛑", "err"),
        4: ("⚠️", "warning"),
        5: ("📣", "notice"),
        6: ("ℹ️", "info"),
        7: ("🐞", "debug"),
    }

    proc = await asyncio.create_subprocess_exec(
        "journalctl", "-f", "-n", "0", "-o", "json",
        stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )

    async def get_formatted(entry: dict):
        ts_str = ""
        ts_us = entry.get("__REALTIME_TIMESTAMP")
        if ts_us is not None:
            try:
                dt = datetime.fromtimestamp(int(ts_us) / 1_000_000, tz=timezone.utc).astimezone()
                ts_str = dt.isoformat(timespec="seconds")
            except Exception:
                ts_str = ""

        try:
            pri = int(entry.get("PRIORITY", 6))
        except Exception:
            pri = 6
        emoji, pname = num_to_meta.get(pri, ("❔", "?"))

        ident = entry.get("SYSLOG_IDENTIFIER") or entry.get("_COMM") or entry.get("_EXE") or "?"
        pid = entry.get("_PID")
        unit = entry.get("_SYSTEMD_UNIT")

        head_parts = [f"{emoji} {pname.upper()}"]
        if ident:
            head_parts.append(f"{ident}{f'[{pid}]' if pid else ''}")
        if unit and unit != ident:
            head_parts.append(f"({unit})")
        if ts_str:
            head_parts.append(f"[{ts_str}]")

        head = " ".join([p for p in head_parts if p])
        msg = str(entry.get("MESSAGE", "")).strip()
        text = f"{head} — {msg}" if msg else head

        return text
    async def send_formatted(text):
        try:
            await bot.send_message(CHAT_ID, text)
        except Exception as e:
            print("Ошибка отправки:", e)

    while True:
        if proc.stdout.at_eof():
            await asyncio.sleep(0.5)
            try:
                proc = await asyncio.create_subprocess_exec(
                    "journalctl", "-f", "-n", "0", "-o", "json",
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE
                )
            except Exception as e:
                print("Не удалось перезапустить journalctl:", e)
                await asyncio.sleep(2)
                continue

        line = await proc.stdout.readline()
        if not line:
            await asyncio.sleep(0.05)
            continue

        try:
            entry = json.loads(line)
        except Exception:
            continue

        cfg = load_config()
        if not cfg.get("enabled", True):
            continue

        try:
            pri = int(entry.get("PRIORITY", 6))
        except Exception:
            pri = 6

        threshold = name_to_num.get(cfg.get("level", "err"), 3)
        text=await get_formatted(entry)
        # Регистронезависимый режим
        if cfg.get("case_insensitive"):
            text_cmp = text.lower()
            kws = [k.lower() for k in (cfg.get("keywords", []) or [])]
            ign = [i.lower() for i in (cfg.get("ignore", []) or [])]
        else:
            text_cmp = text
            kws = cfg.get("keywords", []) or []
            ign = cfg.get("ignore", []) or []

        
        has_kw = any(k in text_cmp for k in kws) if kws else False
        has_ign = any(i in text_cmp for i in ign) if ign else False

        # 1) Достаточно критично — отправляем всегда
        if pri <= threshold:
            
            await send_formatted(text_cmp)
            continue

        # 2) Менее критично — только по ключевым словам и без игнора
        if has_kw and not has_ign:
            await send_formatted(text_cmp)


# === Запуск ===
async def main():
    asyncio.create_task(log_watcher())
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
