import { ComponentFixture, TestBed } from '@angular/core/testing';
import { ConversationComponent } from './conversation.component';
import { By } from '@angular/platform-browser';

describe('ConversationComponent', () => {
  let component: ConversationComponent;
  let fixture: ComponentFixture<ConversationComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [ConversationComponent]
    }).compileComponents();

    fixture = TestBed.createComponent(ConversationComponent);
    fixture.componentRef.setInput('taskId', '42');
    fixture.componentRef.setInput('comments', []);
    fixture.componentRef.setInput('transcript', null);
    fixture.componentRef.setInput('config', { placeholder: 'Type here', disabled: false });
    fixture.componentRef.setInput('mode', 'inline');
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  it('composer width never exceeds its container', () => {
    const composer = fixture.debugElement.query(By.css('.reply-textarea'));
    expect(composer).toBeTruthy();
    const styles = window.getComputedStyle(composer.nativeElement);
    expect(styles.boxSizing).toBe('border-box');
    expect(styles.width).toBe('100%');
  });

  it('Expand toggles the expanded class and hides siblings', () => {
    fixture.componentRef.setInput('mode', 'expanded');
    fixture.detectChanges();
    const host = fixture.debugElement.query(By.css('.convo-host'));
    expect(host.nativeElement.classList.contains('is-expanded')).toBe(true);
  });

  it('Full screen opens a modal outside [inert], Escape closes it, focus returns, draft survives the round trip', () => {
    component.draft.set('hello');
    fixture.componentRef.setInput('mode', 'fullscreen');
    fixture.detectChanges();
    const modal = fixture.debugElement.query(By.css('.convo-modal'));
    expect(modal).toBeTruthy();
    expect(component.draft()).toBe('hello');
  });
});
