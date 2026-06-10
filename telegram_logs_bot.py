#!/usr/bin/env python3
import asyncio
import ipaddress
import json
import os
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional, cast

from aiogram import Bot, Dispatcher, types
from aiogram.client.session.aiohttp import AiohttpSession
from aiogram.filters import Command
from aiogram.methods import TelegramMethod
from aiogram.types import InputFile
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup

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
CONFIG_FILE = Path(__file__).resolve().parents[1] / "config" / "logs_config.json"

# === Конфиг по умолчанию ===
default_config = {
    "level": "err",
    "keywords": ["FAILED", "segfault", "panic", "oom-killer"],
    "ignore": [],
    "enabled": True,
    "case_insensitive": False,
    "awaiting": None,  # None | "keywords" | "ignore"
}


def require_env(name: str) -> str:
    """Return a required environment variable or raise a clear startup error."""
    value = os.environ.get(name)
    if not value:
        raise RuntimeError(f"Missing required environment variable: {name}")
    return value


def require_chat_id(name: str) -> int:
    """Return a required Telegram chat id from the environment."""
    raw_value = require_env(name)
    try:
        return int(raw_value)
    except ValueError as exc:
        raise RuntimeError(
            f"Environment variable {name} must be an integer, got: {raw_value!r}"
        ) from exc


TOKEN = require_env("TELEGRAM_BOT_TOKEN")
CHAT_ID = require_chat_id("TELEGRAM_CHAT_ID")
TELEGRAM_PROXY_URL = os.environ.get("TELEGRAM_PROXY_URL") or os.environ.get("TELEGRAM_PROXY")


def load_config() -> dict:
    """Load the bot config from disk, creating the default config if needed."""
    if CONFIG_FILE.exists():
        try:
            return json.loads(CONFIG_FILE.read_text())
        except Exception:
            pass
    save_config(default_config)
    return default_config


def save_config(cfg: dict) -> None:
    """Persist the bot config to disk."""
    CONFIG_FILE.parent.mkdir(parents=True, exist_ok=True)
    CONFIG_FILE.write_text(json.dumps(cfg, indent=2, ensure_ascii=False))


config = load_config()

# === Инициализация бота ===
class HttpProxySession(AiohttpSession):
    """Aiogram session that routes Telegram API requests through an HTTP proxy."""

    def __init__(self, proxy_url: str, **kwargs: object) -> None:
        super().__init__(**kwargs)
        self.proxy_url = proxy_url

    async def make_request(
        self,
        bot: Bot,
        method: TelegramMethod[object],
        timeout: Optional[int] = None,
    ) -> object:
        session = await self.create_session()
        url = self.api.api_url(token=bot.token, method=method.__api_method__)
        form = self.build_form_data(bot=bot, method=cast(TelegramMethod[InputFile], method))

        try:
            async with session.post(
                url,
                data=form,
                timeout=self.timeout if timeout is None else timeout,
                proxy=self.proxy_url,
            ) as resp:
                raw_result = await resp.text()
        except asyncio.TimeoutError as exc:
            raise RuntimeError("Telegram proxy request timeout") from exc
        except Exception as exc:
            raise RuntimeError(f"Telegram proxy request failed: {exc}") from exc

        response = self.check_response(
            bot=bot,
            method=cast(TelegramMethod[object], method),
            status_code=resp.status,
            content=raw_result,
        )
        return cast(object, response.result)


bot_session = HttpProxySession(TELEGRAM_PROXY_URL) if TELEGRAM_PROXY_URL else AiohttpSession()
bot = Bot(token=TOKEN, session=bot_session)
dp = Dispatcher()


async def run_command(*args: str, timeout: int = 20) -> subprocess.CompletedProcess[str]:
    """Run a command in a thread so bot polling is not blocked."""
    return await asyncio.to_thread(
        subprocess.run,
        list(args),
        text=True,
        capture_output=True,
        timeout=timeout,
        check=False,
    )


def parse_fail2ban_jails(status_output: str) -> list[str]:
    """Extract jail names from `fail2ban-client status` output."""
    match = re.search(r"Jail list:\s*(.+)", status_output)
    if not match:
        return []
    return [jail.strip() for jail in match.group(1).split(",") if jail.strip()]


async def unban_ip_from_all_jails(ip: str) -> tuple[list[str], list[str]]:
    """Unban an IP from every fail2ban jail. Return (unbanned, errors)."""
    try:
        normalized_ip = str(ipaddress.ip_address(ip))
    except ValueError as exc:
        raise ValueError(f"Некорректный IP: {ip}") from exc

    status = await run_command("sudo", "-n", "fail2ban-client", "status")
    if status.returncode != 0:
        details = (status.stderr or status.stdout or "unknown error").strip()
        raise RuntimeError(f"Не удалось получить список jail: {details}")

    jails = parse_fail2ban_jails(status.stdout)
    if not jails:
        raise RuntimeError("Fail2Ban не вернул список jail")

    unbanned: list[str] = []
    errors: list[str] = []
    for jail in jails:
        result = await run_command(
            "sudo", "-n", "fail2ban-client", "set", jail, "unbanip", normalized_ip
        )
        output = (result.stdout or result.stderr or "").strip()
        # fail2ban returns 1 when IP was actually unbanned, and 0 when it was not in that jail.
        if result.returncode == 0:
            continue
        if result.returncode == 1 and output == "1":
            unbanned.append(jail)
            continue
        errors.append(f"{jail}: {output or 'returncode=' + str(result.returncode)}")

    return unbanned, errors

# === UI ===

def main_menu() -> InlineKeyboardMarkup:
    """Build the main settings keyboard."""
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
    await message.answer(
        "⚙️ Настройки логов\n\n"
        "Команды:\n"
        "• /unban <ip> — разбанить IP во всех jail Fail2Ban",
        reply_markup=main_menu(),
    )


@dp.message(Command("cancel"))
async def cmd_cancel(message: types.Message):
    if message.chat.id != CHAT_ID:
        return
    cfg = load_config()
    if cfg.get("awaiting"):
        cfg["awaiting"] = None
        save_config(cfg)
        await message.answer("❎ Редактирование отменено", reply_markup=main_menu())


@dp.message(Command("unban"))
async def cmd_unban(message: types.Message):
    if message.chat.id != CHAT_ID:
        return

    parts = (message.text or "").split(maxsplit=1)
    if len(parts) != 2 or not parts[1].strip():
        await message.answer("Использование: /unban <ip>\nПример: /unban 45.88.172.243")
        return

    ip = parts[1].strip()
    await message.answer(f"🔎 Ищу {ip} во всех jail Fail2Ban и снимаю бан...")
    try:
        unbanned, errors = await unban_ip_from_all_jails(ip)
    except Exception as exc:
        await message.answer(f"❌ Не удалось выполнить unban: {exc}")
        return

    if unbanned:
        text = "✅ Разбанен из jail:\n" + "\n".join(f"• {jail}" for jail in unbanned)
    else:
        text = "ℹ️ Этот IP не был найден в активных ban-list Fail2Ban."

    if errors:
        text += "\n\n⚠️ Ошибки:\n" + "\n".join(errors[:10])
        if len(errors) > 10:
            text += f"\n…ещё {len(errors) - 10}"

    await message.answer(text)


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


async def enter_edit_mode(message: types.Message, field: str) -> None:
    """Switch the chat into incremental or full-list edit mode."""
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


        if (has_kw or pri <= threshold) and not has_ign:
            await send_formatted(text_cmp)


# === Запуск ===
async def main():
    asyncio.create_task(log_watcher())
    await dp.start_polling(bot)


if __name__ == "__main__":
    asyncio.run(main())
