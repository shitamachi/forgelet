// A desktop app, not a console program: Windows must not attach a terminal when
// it is opened from Explorer or a shortcut.
#![windows_subsystem = "windows"]

mod activity;
mod settings;
mod worker;

use activity::{LOG_BYTES, PREVIEW_BYTES, Tail, write_report};
use settings::Settings;
use nsh_base::{ButtonElement, button, input};
use nsh_core::{
    desktop::MenuRole,
    element::desktop::{MenuItem, set_application_menu},
    file_dialog::{FileFilter, SaveDialog},
    prelude::*,
};
use nsh_platform_windows::{AppOptions, AppSource, Root, run_app};
use std::{
    cell::{Cell, RefCell},
    rc::Rc,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use worker::{Connection, Job, Status, Worker};

fn colors(light: u32, dark: u32) -> AdaptiveColor {
    AdaptiveColor::new(rgb(light), rgb(dark))
}

fn ink() -> AdaptiveColor {
    colors(0x222224, 0xf0f0f2)
}

fn secondary() -> AdaptiveColor {
    colors(0x626268, 0xaaaab0)
}

fn separator() -> AdaptiveColor {
    colors(0xd7d7dc, 0x3d3d42)
}

fn accent() -> AdaptiveColor {
    colors(0x0059ba, 0x80b7ff)
}

fn state_label(state: &str) -> &'static str {
    match state {
        "connecting" => "连接中",
        "pairing" => "等待配对",
        "available" | "succeeded" | "failed" | "stopped" | "timed_out" => "空闲",
        "running" => "正在构建",
        "preparing" => "准备执行环境",
        "reconnecting" => "重新连接中",
        "stopping" => "正在停止",
        "error" => "连接失败",
        _ => "已暂停",
    }
}

fn state_color(state: &str) -> AdaptiveColor {
    match state {
        "available" | "succeeded" | "failed" | "stopped" | "timed_out" => {
            colors(0x237150, 0x6dd8a6)
        }
        "running" | "connecting" | "pairing" => accent(),
        "reconnecting" | "stopping" => colors(0x8c570b, 0xffc978),
        "error" => colors(0xa63246, 0xff9eac),
        _ => secondary(),
    }
}

fn job_label(state: &str) -> &'static str {
    match state {
        "succeeded" => "任务通过",
        "failed" => "任务失败",
        "stopped" => "任务停止",
        "timed_out" => "任务超时",
        _ => "正在构建",
    }
}

fn command(label: &'static str) -> ButtonElement {
    button()
        .h(px(25.0))
        .px(px(9.0))
        .items_center()
        .justify_center()
        .rounded(px(5.0))
        .border(px(1.0))
        .border_color(separator())
        .bg(colors(0xfefefe, 0x37373b))
        .hover(|style| style.bg(colors(0xefeff2, 0x47474d)))
        .focus(|style| style.border_color(accent()))
        .active(|style| style.bg(colors(0xe4e4e8, 0x505058)))
        .child(text(label).text_color(ink()).text_size(px(12.0)))
}

fn field(
    label: &'static str,
    placeholder: &'static str,
    value: Signal<String>,
    locked: impl IntoReactive<bool>,
    masked: bool,
) -> impl IntoElement {
    view()
        .gap(px(4.0))
        .child(text(label).text_size(px(11.0)).text_color(secondary()))
        .child(
            input()
                .value(value)
                .placeholder(placeholder)
                .optional_label(Some(SharedString::from(label)))
                .masked(masked)
                .disabled(locked)
                .on_change(move |change: &str| value.set(change.to_owned()))
                .h(px(27.0))
                .px(px(7.0))
                .text_size(px(12.0))
                .text_color(ink())
                .placeholder_color(rgb(0x77777e))
                .rounded(px(5.0))
                .border(px(1.0))
                .border_color(separator())
                .focus(|style| style.border_color(accent()))
                .bg(colors(0xffffff, 0x303033)),
        )
}

#[derive(Clone, Copy, PartialEq)]
enum Pane {
    Log,
    Status,
}

fn tab(name: &'static str, pane: Pane, current: Signal<Pane>) -> impl IntoElement {
    button()
        .h(px(23.0))
        .px(px(12.0))
        .justify_center()
        .rounded(px(4.0))
        .selected(move || current.get() == pane)
        .bg(move || {
            if current.get() == pane {
                colors(0xffffff, 0x57575d)
            } else {
                colors(0xe9e9ed, 0x343438)
            }
        })
        .focus(|style| style.border(px(1.0)).border_color(accent()))
        .on_press(move || current.set(pane))
        .child(text(name).text_size(px(12.0)).text_color(ink()))
}

fn add_status(tail: &RefCell<Tail>, started: Instant, update: &Status) {
    let elapsed = started.elapsed().as_secs();
    let label = match update.state.as_str() {
        "succeeded" | "failed" | "stopped" | "timed_out" => job_label(&update.state),
        state => state_label(state),
    };
    let job = update.job.as_ref().map_or(String::new(), |job| {
        let platform = if job.platform.is_empty() {
            String::new()
        } else {
            format!("  目标 {}", job.platform)
        };
        format!(
            "  {}  {}  {}{platform}",
            job.repository,
            job.name,
            job.sha.chars().take(8).collect::<String>()
        )
    });
    let detail = if update.message.is_empty() {
        String::new()
    } else {
        format!("  {}", update.message)
    };
    tail.borrow_mut().push(format!(
        "+{:02}:{:02}  {label}{job}{detail}\n",
        elapsed / 60,
        elapsed % 60
    ));
}

fn menu(
    toggle: Rc<impl Fn() + 'static>,
    export: Rc<impl Fn() + 'static>,
    active: bool,
    disabled: bool,
    saving: bool,
) {
    use MenuRole::*;
    set_application_menu(vec![
        MenuItem::submenu(
            "文件",
            [
                MenuItem::new("导出运行记录…")
                    .shortcut("mod+shift+s")
                    .disabled(saving)
                    .on_select(move || export()),
                MenuItem::separator(),
                MenuItem::role(Quit),
            ],
        ),
        MenuItem::submenu(
            "编辑",
            [
                MenuItem::role(Undo),
                MenuItem::role(Redo),
                MenuItem::separator(),
                MenuItem::role(Cut),
                MenuItem::role(Copy),
                MenuItem::role(Paste),
                MenuItem::role(SelectAll),
            ],
        ),
        MenuItem::submenu(
            "运行",
            [MenuItem::new(if active { "暂停" } else { "开始" })
                .shortcut("mod+r")
                .disabled(disabled)
                .on_select(move || toggle())],
        ),
        MenuItem::submenu(
            "窗口",
            [
                MenuItem::role(Minimize),
                MenuItem::role(Zoom),
                MenuItem::role(ToggleFullScreen),
                MenuItem::separator(),
                MenuItem::role(Close),
            ],
        ),
    ]);
}

#[component]
fn App() -> impl IntoElement {
    let settings = Settings::load().unwrap_or_default();
    let url = signal(settings.url.clone());
    let image = signal(settings.wslc.image.clone());
    let cpus = signal(
        settings
            .wslc
            .cpus
            .map_or_else(String::new, |value| value.to_string()),
    );
    let memory = signal(settings.wslc.memory.clone().unwrap_or_default());
    let active = signal(false);
    let fields_locked = computed(move || active.get());
    let status = signal(Status::default());
    let job = signal(None::<Job>);
    let job_state = signal(String::new());
    let run_url = signal(String::new());
    let worker = Rc::new(RefCell::new(None::<Worker>));
    let started = Instant::now();
    let timeline = Rc::new(RefCell::new(Tail::new(64 * 1024)));
    add_status(&timeline, started, &Status::default());
    let timeline_text = signal(timeline.borrow().snapshot(PREVIEW_BYTES));
    let logs = Arc::new(Mutex::new(Tail::new(LOG_BYTES)));
    let log_text = signal(String::new());
    let pane = signal(Pane::Log);
    let follow = signal(true);
    let saving = signal(false);
    let feedback = signal(String::new());
    let disabled = computed(move || {
        status.get().state == "stopping"
            || !active.get() && (url.get().trim().is_empty() || image.get().trim().is_empty())
    });

    let record = {
        let timeline = timeline.clone();
        Rc::new(move |update: &Status| {
            add_status(&timeline, started, update);
            timeline_text.set(timeline.borrow().snapshot(PREVIEW_BYTES));
        })
    };
    let set_status = {
        let record = record.clone();
        Rc::new(move |update: Status| {
            if status.get() != update {
                record(&update);
                status.set(update);
            }
        })
    };

    set_interval(Duration::from_millis(180), {
        let worker = worker.clone();
        let logs = logs.clone();
        let set_status = set_status.clone();
        let record = record.clone();
        let last_log = Cell::new(0);
        move || {
            if let Ok(logs) = logs.lock()
                && last_log.get() != logs.revision()
            {
                log_text.set(logs.snapshot(PREVIEW_BYTES));
                last_log.set(logs.revision());
            }
            let mut current = worker.borrow_mut();
            let Some(worker) = current.as_mut() else {
                return;
            };
            let apply_update = |update: Status| {
                if let Some(info) = &update.job {
                    job.set(Some(info.clone()));
                    if matches!(
                        update.state.as_str(),
                        "running" | "succeeded" | "failed" | "stopped" | "timed_out"
                    ) {
                        job_state.set(update.state.clone());
                    }
                }
                if status.get().state != "stopping" || update.state == "error" {
                    set_status(update);
                } else if matches!(
                    update.state.as_str(),
                    "succeeded" | "failed" | "stopped" | "timed_out"
                ) {
                    record(&update);
                }
            };
            for update in worker.updates.try_iter() {
                apply_update(update);
            }
            match worker.exited() {
                Ok(false) => return,
                Ok(true) => {
                    for update in worker.updates.try_iter() {
                        apply_update(update);
                    }
                    if status.get().state != "error" {
                        if job_state.get() == "running" {
                            job_state.set("stopped".into());
                        }
                        set_status(Status::default());
                    }
                }
                Err(_) => set_status(Status {
                    state: "error".into(),
                    message: "无法读取执行器状态".into(),
                    ..Status::default()
                }),
            }
            *current = None;
            active.set(false);
        }
    });

    let toggle = {
        let logs = logs.clone();
        Rc::new(move || {
            if disabled.get() {
                return;
            }
            let mut current = worker.borrow_mut();
            if let Some(worker) = current.as_mut() {
                worker.stop();
                set_status(Status {
                    state: "stopping".into(),
                    ..Status::default()
                });
                return;
            }
            // The app owns its settings; the credential comes from pairing.
            let next = Settings {
                url: url.get().trim().to_owned(),
                wslc: settings::Wslc {
                    image: image.get().trim().to_owned(),
                    cpus: cpus.get().trim().parse::<f64>().ok().filter(|value| *value > 0.0),
                    memory: Some(memory.get().trim().to_owned()).filter(|value| !value.is_empty()),
                    ..settings.wslc.clone()
                },
            };
            if let Err(message) = next.validate().and_then(|()| next.save()) {
                set_status(Status { state: "error".into(), message, ..Status::default() });
                return;
            }
            match Worker::start(
                Connection {
                    url: next.url.trim(),
                    wslc: Some(&next.wslc),
                },
                logs.clone(),
            ) {
                Ok(started) => {
                    run_url.set(next.url.trim().trim_end_matches('/').into());
                    job.set(None);
                    job_state.set(String::new());
                    set_status(Status {
                        state: "connecting".into(),
                        ..Status::default()
                    });
                    active.set(true);
                    *current = Some(started);
                }
                Err(message) => set_status(Status {
                    state: "error".into(),
                    message,
                    ..Status::default()
                }),
            }
        })
    };

    let report = Rc::new(move || {
        let states = timeline.borrow().snapshot(64 * 1024);
        let logs = logs
            .lock()
            .map_or(String::new(), |logs| logs.snapshot(LOG_BYTES));
        format!("Forgelet Runner\n\n状态记录\n{states}\n构建日志\n{logs}")
    });
    let export = {
        let report = report.clone();
        let window_owner = Owner::current();
        Rc::new(move || {
            // The menu is rebuilt when saving changes. Keep the file request
            // and its task attached to the window instead of that menu owner.
            window_owner.run(|| {
                if saving.get() {
                    return;
                }
                saving.set(true);
                let report = report();
                let picked = pick_save_path(
                    SaveDialog::new()
                        .file_name("Forgelet Runner.txt")
                        .filter(FileFilter::new("文本", ["txt"]))
                        .title("导出运行记录")
                        .button_label("导出"),
                );
                spawn(async move {
                    match picked.await {
                        Ok(Some(path)) => {
                            let writer = std::thread::spawn(move || write_report(&path, &report));
                            while !writer.is_finished() {
                                timer(Duration::from_millis(50)).await;
                            }
                            feedback.set(if matches!(writer.join(), Ok(Ok(()))) {
                                "运行记录已导出".into()
                            } else {
                                "无法写入导出文件".into()
                            });
                        }
                        Ok(None) => {}
                        Err(_) => feedback.set("无法打开系统保存面板".into()),
                    }
                    saving.set(false);
                });
            });
        })
    };
    effect({
        let toggle = toggle.clone();
        let export = export.clone();
        move || {
            menu(
                toggle.clone(),
                export.clone(),
                active.get(),
                disabled.get(),
                saving.get(),
            )
        }
    });

    let console = element_ref();
    effect({
        move || {
            let text = match pane.get() {
                Pane::Log => log_text.get(),
                Pane::Status => timeline_text.get(),
            };
            if follow.get() && !text.is_empty() {
                request_animation_frame(move |_| console.scroll_to_end(false));
            }
        }
    });

    view()
        .flex_1()
        .flex_row()
        .bg(colors(0xffffff, 0x1f1f21))
        .child(
            scroll()
                .w(px(224.0))
                .flex_shrink_0()
                .bg(colors(0xf3f3f5, 0x29292c))
                .child(
                    view()
                        .p(px(14.0))
                        .gap(px(13.0))
                        .child(
                            text("本机 Runner")
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_size(px(13.0))
                                .text_color(ink()),
                        )
                        .child(
                            view()
                                .flex_row()
                                .items_center()
                                .gap(px(6.0))
                                .child(
                                    view()
                                        .size(px(7.0))
                                        .rounded_full()
                                        .bg(move || state_color(&status.get().state)),
                                )
                                .child(
                                    text(move || state_label(&status.get().state))
                                        .text_color(secondary())
                                        .text_size(px(12.0)),
                                )
                                .child(view().flex_1())
                                .child(
                                    text("1 个槽位").text_size(px(11.0)).text_color(secondary()),
                                ),
                        )
                        // While an administrator has not approved this machine
                        // yet, the code is the only thing that matters.
                        .child(
                            view()
                                .gap(px(3.0))
                                .child(
                                    text(move || status.get().code.clone())
                                        .font_family("Cascadia Mono")
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .text_size(px(24.0))
                                        .text_color(accent()),
                                )
                                .child(
                                    text(move || {
                                        let current = status.get();
                                        if current.state != "pairing" {
                                            return String::new();
                                        }
                                        if current.message.is_empty() {
                                            "在 Web 的 Runners 页面批准该代码".into()
                                        } else {
                                            current.message
                                        }
                                    })
                                    .text_size(px(11.0))
                                    .text_color(secondary()),
                                ),
                        )
                        .child(
                            button()
                                .disabled(disabled)
                                .on_press(move || toggle())
                                .h(px(27.0))
                                .rounded(px(5.0))
                                .items_center()
                                .justify_center()
                                .bg(move || {
                                    if active.get() {
                                        colors(0xfefefe, 0x39393f)
                                    } else {
                                        colors(0x0865ca, 0x265eaa)
                                    }
                                })
                                .border(px(1.0))
                                .border_color(separator())
                                .focus(|style| style.border_color(accent()))
                                .disabled_style(|style| style.bg(colors(0xe8e8ec, 0x3b3b40)))
                                .child(
                                    text(move || if active.get() { "暂停" } else { "开始" })
                                        .font_weight(FontWeight::SEMIBOLD)
                                        .text_size(px(12.0))
                                        .text_color(move || {
                                            if active.get() || disabled.get() {
                                                ink()
                                            } else {
                                                colors(0xffffff, 0xffffff)
                                            }
                                        }),
                                ),
                        )
                        .child(view().h(px(1.0)).bg(separator()))
                        .child(
                            text("连接设置")
                                .text_color(ink())
                                .text_size(px(12.0))
                                .font_weight(FontWeight::SEMIBOLD),
                        )
                        .child(field(
                            "控制面",
                            "https://forgelet.local",
                            url,
                            fields_locked,
                            false,
                        ))
                        .child(field(
                            "执行镜像",
                            "registry/runner@sha256:…",
                            image,
                            fields_locked,
                            false,
                        ))
                        .child(
                            view()
                                .flex_row()
                                .gap(px(8.0))
                                .child(
                                    view()
                                        .flex_1()
                                        .child(field("CPU（可选）", "2", cpus, fields_locked, false)),
                                )
                                .child(
                                    view()
                                        .flex_1()
                                        .child(field("内存（可选）", "4G", memory, fields_locked, false)),
                                ),
                        )
                        .child(
                            text(move || {
                                let current = status.get();
                                if current.state == "pairing" {
                                    return String::new();
                                }
                                if !current.message.is_empty() {
                                    current.message
                                } else if active.get() {
                                    "暂停、关窗或退出会取消本机当前任务。".into()
                                } else {
                                    "点「开始」，按提示在 Web 中批准本机。".into()
                                }
                            })
                            .text_color(move || {
                                if status.get().state == "error" {
                                    state_color("error")
                                } else {
                                    secondary()
                                }
                            })
                            .text_size(px(11.0)),
                        ),
                ),
        )
        .child(view().w(px(1.0)).bg(separator()))
        .child(
            view()
                .min_w(px(0.0))
                .flex_1()
                .child(
                    view()
                        .flex_row()
                        .items_center()
                        .gap(px(8.0))
                        .px(px(12.0))
                        .h(px(43.0))
                        .bg(colors(0xf6f6f8, 0x2c2c30))
                        .child(
                            view()
                                .flex_row()
                                .gap(px(1.0))
                                .p(px(2.0))
                                .rounded(px(6.0))
                                .bg(colors(0xe5e5ea, 0x343438))
                                .child(tab("日志", Pane::Log, pane))
                                .child(tab("状态", Pane::Status, pane)),
                        )
                        .child(view().flex_1())
                        .child(command("复制记录").on_press(move || {
                            set_clipboard(report());
                            feedback.set("运行记录已复制".into());
                        }))
                        .child(command("导出…").disabled(saving).on_press(move || export())),
                )
                .child(view().h(px(1.0)).bg(separator()))
                .child(
                    view()
                        .px(px(14.0))
                        .py(px(10.0))
                        .flex_row()
                        .items_center()
                        .gap(px(8.0))
                        .child(
                            view()
                                .flex_1()
                                .min_w(px(0.0))
                                .gap(px(3.0))
                                .child(
                                    text(move || {
                                        job.get().map_or_else(|| "暂无任务".into(), |job| job.name)
                                    })
                                    .number_of_lines(1)
                                    .font_weight(FontWeight::SEMIBOLD)
                                    .text_size(px(13.0))
                                    .text_color(ink()),
                                )
                                .child(
                                    text(move || {
                                        job.get().map_or_else(
                                            || "领取任务后，构建输出会显示在下方。".into(),
                                            |job| {
                                                let platform = if job.platform.is_empty() {
                                                    String::new()
                                                } else {
                                                    format!("  ·  目标 {}", job.platform)
                                                };
                                                format!(
                                                    "{}  ·  {}  ·  {}{platform}",
                                                    job.repository,
                                                    job.sha.chars().take(8).collect::<String>(),
                                                    job_label(&job_state.get())
                                                )
                                            },
                                        )
                                    })
                                    .number_of_lines(1)
                                    .text_color(secondary())
                                    .text_size(px(11.0)),
                                ),
                        )
                        .child(
                            command("查看运行")
                                .disabled(move || job.get().is_none())
                                .on_press(move || {
                                    if let Some(job) = job.get()
                                        && job.run_id.bytes().all(|c| {
                                            c.is_ascii_alphanumeric() || c == b'-' || c == b'_'
                                        })
                                    {
                                        let opened = open_url(format!(
                                            "{}/runs/{}",
                                            run_url.get(),
                                            job.run_id
                                        ));
                                        spawn(async move {
                                            let _ = opened.await;
                                        });
                                    }
                                }),
                        ),
                )
                .child(view().h(px(1.0)).bg(separator()))
                .child(
                    scroll()
                        .flex_1()
                        .min_h(px(0.0))
                        .element_ref(&console)
                        .child(
                            view().p(px(13.0)).child(
                                text(move || match pane.get() {
                                    Pane::Log if log_text.get().is_empty() => "暂无构建日志".into(),
                                    Pane::Log => log_text.get(),
                                    Pane::Status => timeline_text.get(),
                                })
                                .font_family("Cascadia Mono")
                                .text_size(px(11.5))
                                .text_color(ink())
                                .selectable(true),
                            ),
                        ),
                )
                .child(view().h(px(1.0)).bg(separator()))
                .child(
                    view()
                        .h(px(31.0))
                        .px(px(12.0))
                        .flex_row()
                        .gap(px(6.0))
                        .items_center()
                        .bg(colors(0xf6f6f8, 0x2c2c30))
                        .child(
                            text(move || {
                                if feedback.get().is_empty() {
                                    "日志与控制面确认的输出同步".into()
                                } else {
                                    feedback.get()
                                }
                            })
                            .text_size(px(11.0))
                            .text_color(secondary()),
                        )
                        .child(view().flex_1())
                        .child(
                            button()
                                .h(px(23.0))
                                .px(px(4.0))
                                .justify_center()
                                .selected(follow)
                                .on_press(move || follow.update(|follow| *follow = !*follow))
                                .child(
                                    text(move || {
                                        if follow.get() {
                                            "跟随最新 ✓"
                                        } else {
                                            "跟随最新"
                                        }
                                    })
                                    .text_size(px(11.0))
                                    .text_color(move || {
                                        if follow.get() { accent() } else { secondary() }
                                    }),
                                ),
                        ),
                ),
        )
}

fn main() -> std::process::ExitCode {
    run_app(
        AppSource::Rust(Root::of::<App>()),
        AppOptions {
            app_id: "io.forgelet.runner".into(),
            title: "Forgelet Runner".into(),
            size: (870, 560),
            ..Default::default()
        },
    )
}
