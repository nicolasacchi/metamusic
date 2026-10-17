// The Judge card is the player while it is on screen: the footer mini-player (and the desktop bar) step aside.
class JudgeCardOwner {
  active = $state(false);
}
export const judgeCard = new JudgeCardOwner();
