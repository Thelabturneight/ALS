package org.libsdl.app;

import android.app.AlertDialog;
import android.content.Context;
import android.content.DialogInterface;
import android.content.SharedPreferences;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.DashPathEffect;
import android.graphics.Paint;
import android.view.InputDevice;
import android.view.LayoutInflater;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewConfiguration;
import android.view.ViewGroup;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.CompoundButton;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.RelativeLayout;
import android.widget.ScrollView;
import android.widget.SeekBar;
import android.widget.Spinner;
import android.widget.TextView;
import android.widget.Toast;

import org.ikemen_engine.ikemen_go.R;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;

/**
 * Overlay de controles touch do Ikemen GO.
 *
 * Alem de emular um gamepad (Xbox 360) por toque, agora e configuravel dentro do jogo:
 *  - segure a engrenagem (canto superior direito) por ~0,6 s para abrir as configuracoes;
 *  - opacidade, tamanho e tolerancia de toque globais;
 *  - modo de edicao: arraste cada controle, toque nele para ajustar tamanho, opacidade,
 *    visibilidade, icone e qual tecla do gamepad ele envia (a, b, c, x, y, z, d, w, start, menu);
 *  - tudo e salvo em SharedPreferences.
 */
public class ControllerOverlay extends RelativeLayout {
    private int virtualDeviceId;
    private int hatX, hatY = 0;
    private final Map<Integer, Set<Integer>> pointerStates = new HashMap<>(); // PointerID -> indices dos controles
    private final Map<Integer, Integer> codeCounts = new HashMap<>();         // codigo -> quantos controles o seguram
    private boolean isInitialized = false;

    // ------------------------------------------------------------------
    // Modelo dos controles
    // ------------------------------------------------------------------
    private static final int TYPE_BUTTON = 0;
    private static final int TYPE_DPAD = 1;
    private static final int TYPE_ANALOG = 2;

    // Codigos do gamepad virtual -> tecla correspondente no Ikemen (config padrao Joystick_P1)
    private static final int[] CODES = {96, 97, 4005, 99, 100, 103, 102, 4004, 108, 109};
    private static final String[] CODE_LABELS = {
            "a  (A)", "b  (B)", "c  (RT)", "x  (X)", "y  (Y)",
            "z  (RB)", "d  (LB)", "w  (LT)", "start", "menu / pausa (BACK)"};

    private static final int[] ICON_RES = {
            R.drawable.btn_punch, R.drawable.btn_palm, R.drawable.btn_ki, R.drawable.btn_run,
            R.drawable.btn_burst, R.drawable.btn_aura, R.drawable.btn_menu};
    private static final int[] ICON_LABEL_RES = {
            R.string.touch_icon_punch, R.string.touch_icon_palm, R.string.touch_icon_ki,
            R.string.touch_icon_run, R.string.touch_icon_burst, R.string.touch_icon_aura,
            R.string.touch_icon_menu};

    private static final float DEF_ALPHA = 0.75f;
    private static final float DEF_SCALE = 1.0f;
    private static final int DEF_TOLERANCE_DP = 8;
    private static final String PREFS_NAME = "ikemen_touch_layout_v1";

    private static class Ctl {
        final String key;
        final int type;
        final int viewId;
        final float baseDp;
        final int defCode;
        final int defIcon;
        final boolean defVisible;

        boolean visible;
        float scale = 1f;
        float alpha = 1f;
        int code;
        int icon;
        float fx = -1f, fy = -1f; // centro em fracao da tela; <0 = posicao padrao
        int shownIcon = -1;
        View view;

        Ctl(String key, int type, int viewId, float baseDp, int defCode, int defIcon, boolean defVisible) {
            this.key = key;
            this.type = type;
            this.viewId = viewId;
            this.baseDp = baseDp;
            this.defCode = defCode;
            this.defIcon = defIcon;
            this.defVisible = defVisible;
            this.visible = defVisible;
            this.code = defCode;
            this.icon = defIcon;
        }
    }

    private final List<Ctl> ctls = new ArrayList<>();
    private Ctl dpadCtl;
    private Ctl analogCtl;

    private float globalAlpha = DEF_ALPHA;
    private float globalScale = DEF_SCALE;
    private int toleranceDp = DEF_TOLERANCE_DP;

    private final float density;
    private final SharedPreferences sp;

    // engrenagem e barra do modo de edicao
    private View gearView;
    private int gearPointerId = -1;
    private final Runnable gearRunnable = new Runnable() {
        @Override
        public void run() {
            gearPointerId = -1;
            showMainDialog();
        }
    };
    private LinearLayout editBar;
    private Button btnEditDone;
    private Button btnEditOptions;

    private boolean editMode = false;
    private Ctl dragCtl;
    private float dragDX, dragDY, downX, downY;
    private boolean dragMoved;
    private final Paint outlinePaint = new Paint(Paint.ANTI_ALIAS_FLAG);

    // ------------------------------------------------------------------
    // Joystick fisico (inalterado)
    // ------------------------------------------------------------------
    public int getPhysicalJoystickCount() {
        int count = 0;
        int[] deviceIds = InputDevice.getDeviceIds();
        for (int id : deviceIds) {
            InputDevice dev = InputDevice.getDevice(id);
            if (isRealGamepad(dev)) {
                count++;
            }
        }
        return count;
    }

    // Helper to distinguish real controllers from "Ghost" sensors
    private boolean isRealGamepad(InputDevice device) {
        if (device == null) return false;

        int sources = device.getSources();
        boolean isJoystick = ((sources & InputDevice.SOURCE_JOYSTICK) == InputDevice.SOURCE_JOYSTICK) ||
                ((sources & InputDevice.SOURCE_GAMEPAD) == InputDevice.SOURCE_GAMEPAD);

        if (!isJoystick) return false;
        if (device.isVirtual()) return false;

        int[] keysToCheck = {
                android.view.KeyEvent.KEYCODE_BUTTON_A,
                android.view.KeyEvent.KEYCODE_BUTTON_B,
                android.view.KeyEvent.KEYCODE_BUTTON_X,
                android.view.KeyEvent.KEYCODE_BUTTON_Y,
                android.view.KeyEvent.KEYCODE_BUTTON_START,
                android.view.KeyEvent.KEYCODE_BUTTON_SELECT,
        };

        boolean[] hasKeys = device.hasKeys(keysToCheck);
        for (boolean exists : hasKeys) {
            if (exists) return true;
        }

        return false;
    }

    // ------------------------------------------------------------------
    // Construcao
    // ------------------------------------------------------------------
    public ControllerOverlay(Context context) {
        super(context);
        this.setClickable(true);
        this.setFocusable(true);
        this.setEnabled(true);
        this.setMotionEventSplittingEnabled(true);

        density = context.getResources().getDisplayMetrics().density;
        sp = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE);

        // Inflate the buttons
        LayoutInflater inflater = LayoutInflater.from(context);
        View vc = inflater.inflate(R.layout.virtual_controller, this, true);
        disableAllTouches(vc);

        buildControlList();
        loadPrefs();
        buildEditBar();

        gearView = findViewById(R.id.btn_gear);

        outlinePaint.setStyle(Paint.Style.STROKE);
        outlinePaint.setStrokeWidth(2f * density);
        outlinePaint.setPathEffect(new DashPathEffect(new float[]{12f * density, 8f * density}, 0f));
    }

    private void buildControlList() {
        dpadCtl = new Ctl("dpad", TYPE_DPAD, R.id.dpad_container, 160, 0, 0, true);
        analogCtl = new Ctl("analog", TYPE_ANALOG, R.id.left_analog, 120, 0, 0, false);
        ctls.add(dpadCtl);
        ctls.add(analogCtl);
        //                       chave     tipo         view          dp  codigo icone visivel
        ctls.add(new Ctl("a",     TYPE_BUTTON, R.id.btn_a,     68,  96,  0, true));
        ctls.add(new Ctl("b",     TYPE_BUTTON, R.id.btn_b,     68,  97,  1, true));
        ctls.add(new Ctl("c",     TYPE_BUTTON, R.id.btn_c,     68, 4005, 2, true));
        ctls.add(new Ctl("y",     TYPE_BUTTON, R.id.btn_y,     68, 100,  3, true));
        ctls.add(new Ctl("x",     TYPE_BUTTON, R.id.btn_x,     68,  99,  4, true));
        ctls.add(new Ctl("start", TYPE_BUTTON, R.id.btn_start, 68, 108,  5, true));
        ctls.add(new Ctl("z",     TYPE_BUTTON, R.id.btn_z,     68, 103,  0, false));
        ctls.add(new Ctl("d",     TYPE_BUTTON, R.id.btn_d,     68, 102,  1, false));
        ctls.add(new Ctl("w",     TYPE_BUTTON, R.id.btn_w,     68, 4004, 2, false));
        ctls.add(new Ctl("menu",  TYPE_BUTTON, R.id.btn_back,  44, 109,  6, true));
        for (Ctl c : ctls) {
            c.view = findViewById(c.viewId);
        }
    }

    private void buildEditBar() {
        Context ctx = getContext();
        editBar = new LinearLayout(ctx);
        editBar.setOrientation(LinearLayout.HORIZONTAL);
        editBar.setBackgroundColor(0xCC000000);
        int p = Math.round(6 * density);
        editBar.setPadding(p, p, p, p);

        btnEditOptions = new Button(ctx);
        btnEditOptions.setText(R.string.touch_options);
        btnEditDone = new Button(ctx);
        btnEditDone.setText(R.string.touch_done);
        editBar.addView(btnEditOptions);
        editBar.addView(btnEditDone);
        disableAllTouches(editBar);
        editBar.setVisibility(View.GONE);

        RelativeLayout.LayoutParams lp = new RelativeLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.addRule(RelativeLayout.ALIGN_PARENT_TOP);
        lp.addRule(RelativeLayout.CENTER_HORIZONTAL);
        addView(editBar, lp);
    }

    private void disableAllTouches(View v) {
        v.setClickable(false);
        v.setFocusable(false);
        if (v instanceof ViewGroup) {
            ViewGroup vg = (ViewGroup) v;
            for (int i = 0; i < vg.getChildCount(); i++) {
                disableAllTouches(vg.getChildAt(i));
            }
        }
    }

    // ------------------------------------------------------------------
    // Preferencias
    // ------------------------------------------------------------------
    private void loadPrefs() {
        globalAlpha = sp.getFloat("g_alpha", DEF_ALPHA);
        globalScale = sp.getFloat("g_scale", DEF_SCALE);
        toleranceDp = sp.getInt("g_tol", DEF_TOLERANCE_DP);
        for (Ctl c : ctls) {
            c.visible = sp.getBoolean(c.key + "_v", c.defVisible);
            c.scale = sp.getFloat(c.key + "_s", 1f);
            c.alpha = sp.getFloat(c.key + "_a", 1f);
            c.code = sp.getInt(c.key + "_c", c.defCode);
            c.icon = sp.getInt(c.key + "_i", c.defIcon);
            c.fx = sp.getFloat(c.key + "_x", -1f);
            c.fy = sp.getFloat(c.key + "_y", -1f);
            if (c.icon < 0 || c.icon >= ICON_RES.length) c.icon = c.defIcon;
            if (indexOfCode(c.code) < 0) c.code = c.defCode;
        }
    }

    private void savePrefs() {
        SharedPreferences.Editor e = sp.edit();
        e.putFloat("g_alpha", globalAlpha);
        e.putFloat("g_scale", globalScale);
        e.putInt("g_tol", toleranceDp);
        for (Ctl c : ctls) {
            e.putBoolean(c.key + "_v", c.visible);
            e.putFloat(c.key + "_s", c.scale);
            e.putFloat(c.key + "_a", c.alpha);
            e.putInt(c.key + "_c", c.code);
            e.putInt(c.key + "_i", c.icon);
            e.putFloat(c.key + "_x", c.fx);
            e.putFloat(c.key + "_y", c.fy);
        }
        e.apply();
    }

    private void resetCtl(Ctl c) {
        c.visible = c.defVisible;
        c.scale = 1f;
        c.alpha = 1f;
        c.code = c.defCode;
        c.icon = c.defIcon;
        c.fx = -1f;
        c.fy = -1f;
    }

    private void resetAll() {
        globalAlpha = DEF_ALPHA;
        globalScale = DEF_SCALE;
        toleranceDp = DEF_TOLERANCE_DP;
        for (Ctl c : ctls) resetCtl(c);
        sp.edit().clear().apply();
        applyLayout();
    }

    private static int indexOfCode(int code) {
        for (int i = 0; i < CODES.length; i++) {
            if (CODES[i] == code) return i;
        }
        return -1;
    }

    // ------------------------------------------------------------------
    // Layout
    // ------------------------------------------------------------------
    @Override
    protected void onSizeChanged(int w, int h, int oldw, int oldh) {
        super.onSizeChanged(w, h, oldw, oldh);
        post(new Runnable() {
            @Override
            public void run() {
                applyLayout();
            }
        });
    }

    private float[] defaultCenter(String key, int W, int H) {
        float d = density;
        float bs = 68f * d;
        float step = bs + 10f * d;
        float c2x = W - 28f * d - bs / 2f;
        float c1x = c2x - step;
        float c0x = c1x - step;
        float r2y = H - 30f * d - bs / 2f;
        float r1y = r2y - step;
        float r0y = r1y - step;
        switch (key) {
            case "dpad":   return new float[]{40f * d + 80f * d, H - 30f * d - 80f * d};
            case "analog": return new float[]{300f * d, H - 30f * d - 60f * d};
            case "a":      return new float[]{c0x, r1y};
            case "b":      return new float[]{c1x, r1y};
            case "c":      return new float[]{c2x, r1y};
            case "y":      return new float[]{c0x, r2y};
            case "x":      return new float[]{c1x, r2y};
            case "start":  return new float[]{c2x, r2y};
            case "d":      return new float[]{c0x, r0y};
            case "w":      return new float[]{c1x, r0y};
            case "z":      return new float[]{c2x, r0y};
            case "menu":   return new float[]{W / 2f, H - 14f * d - 22f * d};
            default:       return new float[]{W / 2f, H / 2f};
        }
    }

    private void place(View v, float cx, float cy, int size) {
        RelativeLayout.LayoutParams lp = new RelativeLayout.LayoutParams(size, size);
        lp.leftMargin = Math.round(cx - size / 2f);
        lp.topMargin = Math.round(cy - size / 2f);
        v.setLayoutParams(lp);
    }

    private void applyLayout() {
        int W = getWidth();
        int H = getHeight();
        if (W <= 0 || H <= 0) return;

        for (Ctl c : ctls) {
            if (c.view == null) continue;
            boolean show = c.visible || editMode;
            c.view.setVisibility(show ? View.VISIBLE : View.GONE);

            int size = Math.round(c.baseDp * density * globalScale * c.scale);
            float cx, cy;
            if (c.fx >= 0f && c.fy >= 0f) {
                cx = c.fx * W;
                cy = c.fy * H;
            } else {
                float[] p = defaultCenter(c.key, W, H);
                cx = p[0];
                cy = p[1];
            }
            place(c.view, cx, cy, size);

            float a = globalAlpha * c.alpha;
            if (!c.visible) a = 0.35f; // so aparece assim no modo de edicao
            c.view.setAlpha(a);

            if (c.type == TYPE_BUTTON && c.view instanceof ImageView && c.shownIcon != c.icon) {
                ((ImageView) c.view).setImageResource(ICON_RES[c.icon]);
                c.shownIcon = c.icon;
            }
        }

        if (gearView != null) {
            int gs = Math.round(32f * density);
            place(gearView, W - 28f * density, 24f * density, gs);
            gearView.setAlpha(0.35f);
            gearView.setVisibility(editMode ? View.GONE : View.VISIBLE);
        }
        invalidate();
    }

    @Override
    protected void dispatchDraw(Canvas canvas) {
        super.dispatchDraw(canvas);
        if (!editMode) return;
        for (Ctl c : ctls) {
            View v = c.view;
            if (v == null || v.getVisibility() != View.VISIBLE) continue;
            outlinePaint.setColor(c.visible ? 0xFF00E676 : 0xFFFFB300);
            canvas.drawCircle(v.getLeft() + v.getWidth() / 2f, v.getTop() + v.getHeight() / 2f,
                    v.getWidth() / 2f, outlinePaint);
        }
    }

    // ------------------------------------------------------------------
    // Toque
    // ------------------------------------------------------------------
    @Override
    public boolean onInterceptTouchEvent(MotionEvent ev) {
        if (!isInitialized) {
            initializeVirtualController();
            isInitialized = true;
        }
        return true;
    }

    private int leftJoyPointerId = -1;

    @Override
    public boolean onTouchEvent(MotionEvent event) {
        if (editMode) {
            return handleEditTouch(event);
        }

        int action = event.getActionMasked();
        int index = event.getActionIndex();
        int pId = event.getPointerId(index);

        float x = event.getX(index);
        float y = event.getY(index);

        if (action == MotionEvent.ACTION_DOWN || action == MotionEvent.ACTION_POINTER_DOWN) {
            if (gearView != null && isViewAtLocation(gearView, x, y)) {
                // engrenagem: precisa segurar para abrir (evita abrir sem querer na luta)
                gearPointerId = pId;
                postDelayed(gearRunnable, 600);
            } else if (isViewAtLocation(analogCtl.view, x, y) && analogCtl.visible) {
                leftJoyPointerId = pId;
            } else {
                updatePointer(pId, getButtonsAt(x, y));
            }
        } else if (action == MotionEvent.ACTION_MOVE) {
            for (int i = 0; i < event.getPointerCount(); i++) {
                int movePId = event.getPointerId(i);
                if (movePId == gearPointerId) continue;

                float mx = event.getX(i);
                float my = event.getY(i);

                if (movePId == leftJoyPointerId) {
                    updateJoystickLogic(analogCtl.view, mx, my);
                } else {
                    Set<Integer> currentButtons = getButtonsAt(mx, my);
                    Set<Integer> lastButtons = pointerStates.get(movePId);

                    if (!currentButtons.equals(lastButtons)) {
                        updatePointer(movePId, currentButtons);
                    }
                }
            }
        } else if (action == MotionEvent.ACTION_CANCEL) {
            cancelGear();
            releaseAll();
        } else if (action == MotionEvent.ACTION_UP || action == MotionEvent.ACTION_POINTER_UP) {
            if (pId == gearPointerId) {
                cancelGear();
            } else if (pId == leftJoyPointerId) {
                resetJoystick(analogCtl.view);
                leftJoyPointerId = -1;
            } else {
                releasePointer(pId);
            }
        }

        updateDpadState(event);
        return true;
    }

    private void cancelGear() {
        removeCallbacks(gearRunnable);
        gearPointerId = -1;
    }

    private void updateJoystickLogic(View v, float x, float y) {
        if (!(v instanceof JoystickOverlay)) return;
        JoystickOverlay joy = (JoystickOverlay) v;

        float centerX = v.getLeft() + (v.getWidth() / 2f);
        float centerY = v.getTop() + (v.getHeight() / 2f);
        float radius = v.getWidth() / 3f;

        float dx = x - centerX;
        float dy = y - centerY;
        float distance = (float) Math.sqrt(dx * dx + dy * dy);

        if (distance > radius) {
            dx = (dx / distance) * radius;
            dy = (dy / distance) * radius;
        }

        joy.updateVisualPos((v.getWidth() / 2f) + dx, (v.getHeight() / 2f) + dy);
        joy.sendToSDL(dx / radius, dy / radius);
    }

    private void resetJoystick(View v) {
        if (!(v instanceof JoystickOverlay)) return;
        JoystickOverlay joy = (JoystickOverlay) v;
        joy.updateVisualPos(v.getWidth() / 2f, v.getHeight() / 2f);
        joy.sendToSDL(0, 0);
    }

    private void updatePointer(int pointerId, Set<Integer> newIdx) {
        Set<Integer> oldIdx = pointerStates.get(pointerId);
        if (oldIdx == null) oldIdx = new HashSet<>();
        for (Integer old : oldIdx) {
            if (!newIdx.contains(old)) {
                handleControl(old, false);
            }
        }
        for (Integer n : newIdx) {
            if (!oldIdx.contains(n)) {
                handleControl(n, true);
            }
        }
        pointerStates.put(pointerId, newIdx);
    }

    private void releasePointer(int pointerId) {
        Set<Integer> last = pointerStates.remove(pointerId);
        if (last != null) {
            for (Integer idx : last) {
                handleControl(idx, false);
            }
        }
    }

    /** Solta tudo (usado antes de abrir menus e em ACTION_CANCEL) para nao deixar tecla presa. */
    private void releaseAll() {
        List<Integer> ids = new ArrayList<>(pointerStates.keySet());
        for (int id : ids) {
            releasePointer(id);
        }
        codeCounts.clear();
        if (leftJoyPointerId != -1) {
            resetJoystick(analogCtl.view);
            leftJoyPointerId = -1;
        }
        if (hatX != 0 || hatY != 0) {
            hatX = 0;
            hatY = 0;
            SDLControllerManager.onNativeHat(virtualDeviceId, 0, 0, 0);
            updateDpadVisual(0, 0);
        }
    }

    /** Pressiona/solta um controle; dois controles mapeados para o mesmo codigo nao se atrapalham. */
    private void handleControl(int idx, boolean pressed) {
        if (idx < 0 || idx >= ctls.size()) return;
        Ctl c = ctls.get(idx);
        int code = c.code;
        Integer cnt = codeCounts.get(code);
        int n = (cnt == null) ? 0 : cnt;
        if (pressed) {
            codeCounts.put(code, n + 1);
            if (n == 0) sendCode(code, true);
        } else if (n > 0) {
            codeCounts.put(code, n - 1);
            if (n == 1) sendCode(code, false);
        }
        setPressedVisual(c.view, pressed ? 120 : 0);
    }

    private void sendCode(final int code, boolean pressed) {
        if (code >= 4000) { // TRIGGERS
            int axis = code - 4000;
            SDLControllerManager.onNativeJoy(virtualDeviceId, axis, pressed ? 1.0f : 0.0f);
        } else { // STANDARD BUTTONS
            if (pressed) {
                SDLControllerManager.onNativeHat(virtualDeviceId, 0, hatX, hatY);
                int result = SDLControllerManager.onNativePadDown(virtualDeviceId, code);
                if (result < 0) {
                    android.util.Log.e("SDL", "INPUT FAILURE: Device " + virtualDeviceId + " rejected button " + code);
                }
            } else {
                SDLControllerManager.onNativePadUp(virtualDeviceId, code);
            }
        }
    }

    private void setPressedVisual(View v, int darkAlpha) {
        if (!(v instanceof ImageView)) return;
        if (darkAlpha > 0) {
            ((ImageView) v).setColorFilter(Color.argb(darkAlpha, 0, 0, 0));
        } else {
            ((ImageView) v).clearColorFilter();
        }
    }

    private void updateDpadState(MotionEvent event) {
        int newX = 0, newY = 0;
        int action = event.getActionMasked();

        if (action != MotionEvent.ACTION_UP && action != MotionEvent.ACTION_CANCEL
                && dpadCtl.visible && dpadCtl.view != null) {
            View container = dpadCtl.view;

            float centerX = container.getLeft() + (container.getWidth() / 2f);
            float centerY = container.getTop() + (container.getHeight() / 2f);
            float radius = container.getWidth() / 2f;

            for (int i = 0; i < event.getPointerCount(); i++) {
                if (action == MotionEvent.ACTION_POINTER_UP && i == event.getActionIndex()) continue;
                int pid = event.getPointerId(i);
                if (pid == gearPointerId || pid == leftJoyPointerId) continue;

                float x = event.getX(i);
                float y = event.getY(i);

                float dx = x - centerX;
                float dy = y - centerY;
                float distance = (float) Math.sqrt(dx * dx + dy * dy);

                // Zona morta no centro e limite externo
                if (distance > (radius * 0.2f) && distance < (radius * 1.3f)) {
                    double angle = Math.toDegrees(Math.atan2(dy, dx));
                    if (angle < 0) angle += 360;

                    // 8 direcoes: cardeais 30 graus, diagonais 60 graus
                    if (angle >= 345 || angle < 15)        newX = 1;                // Direita
                    else if (angle >= 15  && angle < 75)   { newX = 1; newY = 1; }  // Baixo-Direita
                    else if (angle >= 75  && angle < 105)  newY = 1;                // Baixo
                    else if (angle >= 105 && angle < 165)  { newX = -1; newY = 1; } // Baixo-Esquerda
                    else if (angle >= 165 && angle < 195)  newX = -1;               // Esquerda
                    else if (angle >= 195 && angle < 255)  { newX = -1; newY = -1; } // Cima-Esquerda
                    else if (angle >= 255 && angle < 285)  newY = -1;               // Cima
                    else if (angle >= 285 && angle < 345)  { newX = 1; newY = -1; } // Cima-Direita

                    break; // um dedo basta para o D-pad
                }
            }
        }

        if (newX != hatX || newY != hatY) {
            hatX = newX;
            hatY = newY;
            SDLControllerManager.onNativeHat(virtualDeviceId, 0, hatX, hatY);
            updateDpadVisual(hatX, hatY);
        }
    }

    private void updateDpadVisual(int hX, int hY) {
        setPressedVisual(dpadCtl.view, (hX != 0 || hY != 0) ? 70 : 0);
    }

    private Set<Integer> getButtonsAt(float x, float y) {
        Set<Integer> found = new HashSet<>();
        float pad = toleranceDp * density;
        for (int i = 0; i < ctls.size(); i++) {
            Ctl c = ctls.get(i);
            if (c.type != TYPE_BUTTON || !c.visible) continue;
            View v = c.view;
            if (v == null || v.getVisibility() != View.VISIBLE) continue;
            float cx = v.getLeft() + v.getWidth() / 2f;
            float cy = v.getTop() + v.getHeight() / 2f;
            float r = v.getWidth() / 2f + pad;
            float dx = x - cx;
            float dy = y - cy;
            if (dx * dx + dy * dy <= r * r) {
                found.add(i);
            }
        }
        return found;
    }

    // Helper to check if a finger is inside a specific view's area
    private boolean isViewAtLocation(View v, float x, float y) {
        if (v == null || v.getVisibility() != View.VISIBLE) return false;

        int[] vLoc = new int[2];
        v.getLocationOnScreen(vLoc);

        int[] parentLoc = new int[2];
        this.getLocationOnScreen(parentLoc);

        int relativeLeft = vLoc[0] - parentLoc[0];
        int relativeTop = vLoc[1] - parentLoc[1];

        return x >= relativeLeft && x <= (relativeLeft + v.getWidth()) &&
                y >= relativeTop && y <= (relativeTop + v.getHeight());
    }

    // ------------------------------------------------------------------
    // Modo de edicao (arrastar / tocar para ajustar)
    // ------------------------------------------------------------------
    private void enterEditMode() {
        releaseAll();
        editMode = true;
        editBar.setVisibility(View.VISIBLE);
        applyLayout();
        Toast.makeText(getContext(), R.string.touch_edit_hint, Toast.LENGTH_LONG).show();
    }

    private void exitEditMode() {
        editMode = false;
        dragCtl = null;
        editBar.setVisibility(View.GONE);
        savePrefs();
        applyLayout();
    }

    private Ctl findCtlAt(float x, float y) {
        for (int i = ctls.size() - 1; i >= 0; i--) {
            Ctl c = ctls.get(i);
            View v = c.view;
            if (v == null || v.getVisibility() != View.VISIBLE) continue;
            float cx = v.getLeft() + v.getWidth() / 2f;
            float cy = v.getTop() + v.getHeight() / 2f;
            float r = v.getWidth() / 2f;
            float dx = x - cx;
            float dy = y - cy;
            if (dx * dx + dy * dy <= r * r) return c;
        }
        return null;
    }

    private boolean handleEditTouch(MotionEvent e) {
        float x = e.getX();
        float y = e.getY();
        switch (e.getActionMasked()) {
            case MotionEvent.ACTION_DOWN:
                if (isViewAtLocation(btnEditDone, x, y)) {
                    exitEditMode();
                    return true;
                }
                if (isViewAtLocation(btnEditOptions, x, y)) {
                    showMainDialog();
                    return true;
                }
                dragCtl = findCtlAt(x, y);
                if (dragCtl != null) {
                    View v = dragCtl.view;
                    float cx = v.getLeft() + v.getWidth() / 2f;
                    float cy = v.getTop() + v.getHeight() / 2f;
                    dragDX = cx - x;
                    dragDY = cy - y;
                    downX = x;
                    downY = y;
                    dragMoved = false;
                }
                return true;
            case MotionEvent.ACTION_MOVE:
                if (dragCtl != null) {
                    if (!dragMoved) {
                        float slop = ViewConfiguration.get(getContext()).getScaledTouchSlop();
                        float mx = x - downX;
                        float my = y - downY;
                        if (mx * mx + my * my > slop * slop) dragMoved = true;
                    }
                    if (dragMoved) {
                        int W = getWidth();
                        int H = getHeight();
                        float nx = Math.max(0f, Math.min(W, x + dragDX));
                        float ny = Math.max(0f, Math.min(H, y + dragDY));
                        dragCtl.fx = nx / W;
                        dragCtl.fy = ny / H;
                        applyLayout();
                    }
                }
                return true;
            case MotionEvent.ACTION_UP:
                if (dragCtl != null) {
                    Ctl c = dragCtl;
                    dragCtl = null;
                    if (dragMoved) {
                        savePrefs();
                    } else {
                        showCtlDialog(c);
                    }
                }
                return true;
            case MotionEvent.ACTION_CANCEL:
                dragCtl = null;
                return true;
            default:
                return true;
        }
    }

    // ------------------------------------------------------------------
    // Dialogos de configuracao
    // ------------------------------------------------------------------
    private interface OnValue {
        void onValue(int v);
    }

    private String str(int id) {
        return getContext().getString(id);
    }

    private LinearLayout newColumn(Context c) {
        LinearLayout col = new LinearLayout(c);
        col.setOrientation(LinearLayout.VERTICAL);
        int p = Math.round(16 * density);
        col.setPadding(p, p / 2, p, 0);
        return col;
    }

    private void addSlider(LinearLayout parent, Context c, final int labelRes,
                           final int min, int max, int value, final OnValue cb) {
        final TextView tv = new TextView(c);
        tv.setText(getContext().getString(labelRes, value));
        tv.setPadding(0, Math.round(10 * density), 0, 0);
        SeekBar sb = new SeekBar(c);
        sb.setMax(max - min);
        sb.setProgress(value - min);
        sb.setOnSeekBarChangeListener(new SeekBar.OnSeekBarChangeListener() {
            @Override
            public void onProgressChanged(SeekBar s, int progress, boolean fromUser) {
                int v = progress + min;
                tv.setText(getContext().getString(labelRes, v));
                if (fromUser) cb.onValue(v);
            }

            @Override
            public void onStartTrackingTouch(SeekBar s) {
            }

            @Override
            public void onStopTrackingTouch(SeekBar s) {
            }
        });
        parent.addView(tv);
        parent.addView(sb);
    }

    private void addSpinner(LinearLayout parent, Context c, int labelRes, String[] items,
                            int selected, final OnValue cb) {
        TextView tv = new TextView(c);
        tv.setText(labelRes);
        tv.setPadding(0, Math.round(10 * density), 0, 0);
        Spinner spin = new Spinner(c);
        ArrayAdapter<String> ad = new ArrayAdapter<String>(c, android.R.layout.simple_spinner_item, items);
        ad.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
        spin.setAdapter(ad);
        spin.setSelection(Math.max(0, selected));
        spin.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            @Override
            public void onItemSelected(AdapterView<?> p, View v, int pos, long id) {
                cb.onValue(pos);
            }

            @Override
            public void onNothingSelected(AdapterView<?> p) {
            }
        });
        parent.addView(tv);
        parent.addView(spin);
    }

    private void showMainDialog() {
        releaseAll();
        AlertDialog.Builder b = new AlertDialog.Builder(getContext(),
                android.R.style.Theme_DeviceDefault_Dialog_Alert);
        Context dc = b.getContext();
        ScrollView sv = new ScrollView(dc);
        LinearLayout root = newColumn(dc);
        sv.addView(root);

        addSlider(root, dc, R.string.touch_opacity, 10, 100, Math.round(globalAlpha * 100f), new OnValue() {
            @Override
            public void onValue(int v) {
                globalAlpha = v / 100f;
                applyLayout();
            }
        });
        addSlider(root, dc, R.string.touch_size, 50, 150, Math.round(globalScale * 100f), new OnValue() {
            @Override
            public void onValue(int v) {
                globalScale = v / 100f;
                applyLayout();
            }
        });
        addSlider(root, dc, R.string.touch_tolerance, 0, 24, toleranceDp, new OnValue() {
            @Override
            public void onValue(int v) {
                toleranceDp = v;
            }
        });

        b.setTitle(R.string.touch_settings_title);
        b.setView(sv);
        b.setPositiveButton(R.string.touch_close, null);
        b.setNeutralButton(R.string.touch_edit_layout, new DialogInterface.OnClickListener() {
            @Override
            public void onClick(DialogInterface d, int which) {
                enterEditMode();
            }
        });
        b.setNegativeButton(R.string.touch_reset, new DialogInterface.OnClickListener() {
            @Override
            public void onClick(DialogInterface d, int which) {
                confirmResetAll();
            }
        });
        b.setOnDismissListener(new DialogInterface.OnDismissListener() {
            @Override
            public void onDismiss(DialogInterface d) {
                savePrefs();
            }
        });
        b.show();
    }

    private void confirmResetAll() {
        new AlertDialog.Builder(getContext(), android.R.style.Theme_DeviceDefault_Dialog_Alert)
                .setMessage(R.string.touch_reset_confirm)
                .setPositiveButton(android.R.string.ok, new DialogInterface.OnClickListener() {
                    @Override
                    public void onClick(DialogInterface d, int which) {
                        resetAll();
                    }
                })
                .setNegativeButton(android.R.string.cancel, null)
                .show();
    }

    private String ctlLabel(Ctl c) {
        if (c.type == TYPE_DPAD) return str(R.string.touch_ctl_dpad);
        if (c.type == TYPE_ANALOG) return str(R.string.touch_ctl_analog);
        return getContext().getString(R.string.touch_ctl_button, c.key.toUpperCase(Locale.ROOT));
    }

    private void showCtlDialog(final Ctl c) {
        releaseAll();
        AlertDialog.Builder b = new AlertDialog.Builder(getContext(),
                android.R.style.Theme_DeviceDefault_Dialog_Alert);
        Context dc = b.getContext();
        ScrollView sv = new ScrollView(dc);
        LinearLayout root = newColumn(dc);
        sv.addView(root);

        addSlider(root, dc, R.string.touch_size, 40, 250, Math.round(c.scale * 100f), new OnValue() {
            @Override
            public void onValue(int v) {
                c.scale = v / 100f;
                applyLayout();
            }
        });
        addSlider(root, dc, R.string.touch_opacity, 10, 100, Math.round(c.alpha * 100f), new OnValue() {
            @Override
            public void onValue(int v) {
                c.alpha = v / 100f;
                applyLayout();
            }
        });

        CheckBox cb = new CheckBox(dc);
        cb.setText(R.string.touch_visible);
        cb.setChecked(c.visible);
        cb.setOnCheckedChangeListener(new CompoundButton.OnCheckedChangeListener() {
            @Override
            public void onCheckedChanged(CompoundButton button, boolean checked) {
                c.visible = checked;
                applyLayout();
            }
        });
        root.addView(cb);

        if (c.type == TYPE_BUTTON) {
            addSpinner(root, dc, R.string.touch_sends, CODE_LABELS, indexOfCode(c.code), new OnValue() {
                @Override
                public void onValue(int pos) {
                    c.code = CODES[pos];
                }
            });
            String[] iconLabels = new String[ICON_LABEL_RES.length];
            for (int i = 0; i < iconLabels.length; i++) {
                iconLabels[i] = getContext().getString(ICON_LABEL_RES[i]);
            }
            addSpinner(root, dc, R.string.touch_icon, iconLabels, c.icon, new OnValue() {
                @Override
                public void onValue(int pos) {
                    c.icon = pos;
                    applyLayout();
                }
            });
        }

        b.setTitle(ctlLabel(c));
        b.setView(sv);
        b.setPositiveButton(android.R.string.ok, null);
        b.setNegativeButton(R.string.touch_reset, new DialogInterface.OnClickListener() {
            @Override
            public void onClick(DialogInterface d, int which) {
                resetCtl(c);
                applyLayout();
                savePrefs();
            }
        });
        b.setOnDismissListener(new DialogInterface.OnDismissListener() {
            @Override
            public void onDismiss(DialogInterface d) {
                savePrefs();
            }
        });
        b.show();
    }

    // ------------------------------------------------------------------
    // Inicializacao do gamepad virtual
    // ------------------------------------------------------------------
    public void initializeVirtualController() {
        android.util.Log.i("ControllerOverlay", "DEBUG: initializing virtual joystick...");
        virtualDeviceId = getPhysicalJoystickCount();
        pointerStates.clear();
        codeCounts.clear();
        hatX = 0;
        hatY = 0;
        android.util.Log.i("ControllerOverlay", String.format("DEBUG: virtualDeviceID = %d", virtualDeviceId));
        SDLControllerManager.nativeRemoveJoystick(virtualDeviceId);
        ensureJoystickAlive();

        View ls = analogCtl.view;
        if (ls instanceof JoystickOverlay) {
            ((JoystickOverlay) ls).setAttrs(virtualDeviceId, 0, 1);
        }

        for (int i = 0; i < 6; i++) {
            SDLControllerManager.onNativeJoy(virtualDeviceId, i, 0.00390625f);
            SDLControllerManager.onNativeJoy(virtualDeviceId, i, 0.0f);
        }
        SDLControllerManager.onNativeHat(virtualDeviceId, 0, 0, 0);
        android.util.Log.i("ControllerOverlay", "DEBUG: virtual joystick initialized!");
        isInitialized = true;
    }

    private int ensureJoystickAlive() {
        return SDLControllerManager.nativeAddJoystick(virtualDeviceId, "Xbox 360 Controller", "Gamepad", 0x045E, 0x028E, false, 0xFFF, 6, 0x3F, 1, 0);
    }
}
